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
use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
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

/// One model field the STATELESS SIGNED SESSION carries. It is an identity field
/// whose resolved type is nominally `Session` / `Maybe Session` AND which some
/// server branch writes (so a trusted server-side value exists to sign). Under
/// `--target web:app` the backend signs its value into an httpOnly `sky_sid`
/// cookie on the establishing branch and VERIFIES that cookie on every RPC + SSR,
/// taking the value from the cookie — never from the forgeable wire model. No
/// server session store, so the backend stays stateless and scales, whilst the
/// end-user experience matches the Sky.Live target (which holds the session
/// server-side per `sky_sid`).
struct SessionProjField {
    /// The model field name (`session`).
    name: String,
    /// The `Std.Codec` expression that round-trips the field value. It is
    /// resolved through the SAME resolver the wire records use (an auto-derived
    /// or user codec), never a hand-rolled rt.Coerce, so the value round-trips
    /// soundly. Emitted into `Shared` as `spaSessionCodec<Field>_`.
    codec: String,
    /// The field's surface type (`Maybe Session`) for the codec's annotation.
    surface: String,
}

/// Uppercase the first character of `s` (`session` → `Session`). Used to build
/// the per-field binding names (`spaSessionCodecSession_`, `verifiedSession_`).
fn cap_first(s: &str) -> String {
    let mut cs = s.chars();
    match cs.next() {
        Some(c) => c.to_uppercase().collect::<String>() + cs.as_str(),
        None => String::new(),
    }
}

/// The `Shared`-exported codec binding name for one identity field.
fn session_codec_name(field: &str) -> String {
    format!("spaSessionCodec{}_", cap_first(field))
}

/// The backend verify-helper name for one identity field.
fn session_verify_name(field: &str) -> String {
    format!("verified{}_", cap_first(field))
}

/// True when a model field's resolved type is nominally `Session` or
/// `Maybe Session` — the identity projection the signed session carries. Mirrors
/// [`field_ty_codec`]'s nominal-tail match; a structural record row (the solver
/// expands a record alias to an un-named row) is recovered back to its nominal
/// name via the project shapes, so `session : Maybe Session` matches whether the
/// solver left `Session` nominal or expanded it.
fn is_session_identity_ty(t: &ty::Ty, shapes: &ProjectShapes) -> bool {
    let inner = match t {
        ty::Ty::App(name, args) if tail_seg(name.as_str()) == "Maybe" && args.len() == 1 => {
            &args[0]
        }
        _ => t,
    };
    match inner {
        ty::Ty::App(name, args) if args.is_empty() => tail_seg(name.as_str()) == "Session",
        ty::Ty::Record(fields, _) => {
            let set: BTreeSet<String> =
                fields.iter().map(|(n, _)| n.as_str().to_string()).collect();
            shapes
                .record_by_fields
                .get(&set)
                .map(|n| n == "Session")
                .unwrap_or(false)
        }
        _ => false,
    }
}

/// The default-value CLASS of one declared record field, precomputed from the
/// project's CST (§14 #2, option B). It drives the synthesised nominal blank
/// (`blank<N>_ : <N>`) that seeds an auto-derived `Codec.auto` codec: every kind
/// here has a sound zero-value; anything else is [`FieldKind::Unsupported`] and
/// forces the fallback error (option A — "declare a top-level `Codec <T>`").
enum FieldKind {
    Str,
    Int,
    Float,
    Bool,
    /// `List _` → `[]` (element type is coerced by the top-level annotation).
    ListLike,
    /// `Maybe _` → `Nothing`.
    MaybeLike,
    /// A nested project RECORD → recurse an inline `{ … }` blank.
    Record(String),
    /// A project data union / enum → its FIRST nullary constructor (or the
    /// fallback error when the union has no nullary constructor).
    Union(String),
    /// Any shape with no synthesisable default (a function, a tuple, `Result`,
    /// `Dict`, `Secret`, `Set`, `Decimal`, an anonymous inline record, …). Carries
    /// the surface rendering for the actionable error.
    Unsupported(String),
}

/// Structural facts about the project's own type declarations, read once from the
/// CST, that let the codec resolver AUTO-DERIVE a record codec (§14 #2, option B):
///   * recover a nominal record NAME from a structural `ty::Ty::Record` (the
///     solver expands a record alias to an un-named row) by matching its field
///     SET, and
///   * synthesise a sound nominal blank for `Codec.auto` from the record's
///     declared fields, recursing into nested records and defaulting a union
///     field to its first nullary constructor.
struct ProjectShapes {
    /// Nominal record name → its declared fields (source order) + default class.
    records: HashMap<String, Vec<(String, FieldKind)>>,
    /// Field-NAME set → the unique nominal record with exactly those fields. A
    /// set shared by two records is AMBIGUOUS and omitted (recovery then fails
    /// closed to the actionable error rather than guess).
    record_by_fields: HashMap<BTreeSet<String>, String>,
    /// Union name → its constructors `(name, is_nullary)` in declared order.
    unions: HashMap<String, Vec<(String, bool)>>,
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
    /// The project's record + union shapes — the raw material for auto-derive.
    shapes: &'a ProjectShapes,
    /// Names of user codec bindings referenced (→ copied into `Shared`).
    needed: BTreeSet<String>,
    /// Record names the resolver AUTO-DERIVED a `Codec.auto` codec for → the
    /// synthesised nominal blank RECORD literal body. Rendered into `Shared` as
    /// `blank<N>_ : <N>` + `auto<N>Codec_ = Codec.auto blank<N>_`; the names are
    /// also fed into the type-copy seed so `<N>` reaches `Shared`.
    auto_records: BTreeMap<String, String>,
    /// The records currently being blank-synthesised — the recursion guard for a
    /// self-referential record (`{ next : Node }`), which has no finite blank.
    synth_stack: Vec<String>,
}

impl<'a> CodecResolver<'a> {
    fn new(registry: &'a [CodecBinding], shapes: &'a ProjectShapes) -> Self {
        CodecResolver {
            registry,
            shapes,
            needed: BTreeSet::new(),
            auto_records: BTreeMap::new(),
            synth_stack: Vec::new(),
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
            // (b') `Result e a` from the error + value codecs. `Std.Codec.result`
            // is a stdlib combinator (imported into Shared like `Codec.int`), so a
            // `Result Error String` message payload (the `Cmd.perform … Sent`
            // shape) crosses the wire with no hand-written app codec.
            if tail == "Result" && args.len() == 2 {
                let err = self.resolve(&args[0])?;
                let val = self.resolve(&args[1])?;
                return Ok(ResolvedCodec {
                    codec: format!("(Codec.result {} {})", err.codec, val.codec),
                    surface: format!(
                        "Result {} {}",
                        wrap_arg(&err.surface),
                        wrap_arg(&val.surface)
                    ),
                });
            }
            // (b'') The stdlib `Error` type — the canonical `Std.Codec.error`
            // codec. `Error` is a globally auto-imported kernel type, so the
            // generated field type needs no import; `Codec.error` rides the same
            // `import Std.Codec as Codec` the primitive codecs assume.
            if tail == "Error" && args.is_empty() {
                return Ok(ResolvedCodec {
                    codec: "Codec.error".to_string(),
                    surface: "Error".to_string(),
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
            // (b''') A NOMINAL that resolves to a project RECORD declaration
            // (`ty::Ty::App(name, [])` the solver left un-expanded) — auto-derive
            // a `Codec.auto` codec for it (§14 #2, option B).
            if args.is_empty() && self.shapes.records.contains_key(tail) {
                return self.auto_derive_record(tail.to_string());
            }
            // A bare data-carrying / enum UNION cannot cross the wire under
            // `Codec.auto`: `codecAutoDecodeVal` has NO rebuild path for a
            // top-level union (only the struct-FIELD path decodes an ADT), so
            // fail closed with the actionable "declare a Codec" instruction
            // (option A). A union nested INSIDE a record is fine — that path is
            // reached via the record blank, not here.
            if self.shapes.unions.contains_key(tail) {
                return Err(bare_union_no_codec_msg(tail, &render_ty(t)));
            }
        }
        // (b'''') A STRUCTURAL record — the common case, because the solver
        // expands a record alias to an un-named `ty::Ty::Record` row. Recover the
        // nominal name by matching the field SET, then auto-derive `Codec.auto`.
        if let ty::Ty::Record(fields, _) = t {
            let set: BTreeSet<String> =
                fields.iter().map(|(n, _)| n.as_str().to_string()).collect();
            if let Some(name) = self.shapes.record_by_fields.get(&set).cloned() {
                return self.auto_derive_record(name);
            }
            // A record whose nominal name we cannot recover (an anonymous inline
            // record, or a field-set shared by two named records) — fail closed
            // with the actionable instruction rather than guess.
            let shown: Vec<String> = set.into_iter().collect();
            return Err(format!(
                "no codec for an anonymous record `{{ {} }}` — `Codec.auto` needs a named type. Give it a top-level `type alias` and a `Codec <T>` binding in the project (spa-split copies it into Shared).",
                shown.join(", ")
            ));
        }
        // (d) No codec — fail closed with an actionable message.
        Err(format!(
            "no codec for a field of type `{0}` — define a top-level `Codec {0}` binding in the project (spa-split copies it into Shared) or reduce the field to a record / `List` / `Maybe` / `Int` / `String` / `Bool` / `Float`",
            render_ty(t)
        ))
    }

    /// Record that record `name` needs an auto-derived `Codec.auto` codec, and
    /// return the reference to emit (`auto<N>Codec_`) + the nominal surface. The
    /// synthesised nominal blank (`blank<N>_ : <N>`) is built once and cached in
    /// [`CodecResolver::auto_records`]; a self-referential record is refused
    /// (fail closed) rather than looped.
    fn auto_derive_record(&mut self, name: String) -> Result<ResolvedCodec, String> {
        if !self.auto_records.contains_key(&name) {
            let body = self.synth_blank_literal(&name)?;
            self.auto_records.insert(name.clone(), body);
        }
        Ok(ResolvedCodec {
            codec: format!("auto{name}Codec_"),
            surface: name,
        })
    }

    /// Synthesise the sound blank RECORD literal for a named project record —
    /// `{ f1 = <default>, … }`. Recurses into nested records (an inline blank,
    /// its element types coerced by the enclosing top-level annotation) and
    /// defaults a union field to its first nullary constructor. Fails closed if
    /// any field has no synthesisable default, or on a self-referential record.
    fn synth_blank_literal(&mut self, name: &str) -> Result<String, String> {
        if self.synth_stack.iter().any(|n| n == name) || self.synth_stack.len() > 32 {
            return Err(format!(
                "no auto-derivable codec for the self-referential record `{name}` — `Codec.auto` needs a finite blank. Define a top-level `Codec {name}` binding in the project (spa-split copies it into Shared)."
            ));
        }
        // `self.shapes` is a `&'a ProjectShapes` that outlives this `&mut self`
        // call, so copying the reference out lets the recursive
        // `default_for_kind(&mut self, …)` run while we read the field list.
        let shapes: &'a ProjectShapes = self.shapes;
        let fields = shapes.records.get(name).ok_or_else(|| {
            format!("no codec for `{name}` — it is not a project record type; define a top-level `Codec {name}` binding in the project.")
        })?;
        if fields.is_empty() {
            return Ok("{}".to_string());
        }
        self.synth_stack.push(name.to_string());
        let mut parts: Vec<String> = Vec::with_capacity(fields.len());
        for (fname, kind) in fields {
            let default = self.default_for_kind(kind, name, fname)?;
            parts.push(format!("{fname} = {default}"));
        }
        self.synth_stack.pop();
        Ok(render_record_literal(&parts))
    }

    /// The default expression for one field kind (`String` → `""`, a nested
    /// record → an inline blank, a union → its first nullary constructor).
    fn default_for_kind(
        &mut self,
        kind: &FieldKind,
        owner: &str,
        field: &str,
    ) -> Result<String, String> {
        Ok(match kind {
            FieldKind::Str => "\"\"".to_string(),
            FieldKind::Int => "0".to_string(),
            FieldKind::Float => "0.0".to_string(),
            FieldKind::Bool => "False".to_string(),
            FieldKind::ListLike => "[]".to_string(),
            FieldKind::MaybeLike => "Nothing".to_string(),
            FieldKind::Record(inner) => self.synth_blank_literal(inner)?,
            FieldKind::Union(u) => {
                let ctor = self
                    .shapes
                    .unions
                    .get(u)
                    .and_then(|cs| cs.iter().find(|(_, nullary)| *nullary))
                    .map(|(n, _)| n.clone());
                match ctor {
                    Some(c) => c,
                    None => {
                        return Err(format!(
                            "no auto-derivable blank for field `{field}` of `{owner}`: its type `{u}` is a union with no nullary constructor, so there is no default value. Define a top-level `Codec {owner}` binding in the project (spa-split copies it into Shared)."
                        ))
                    }
                }
            }
            FieldKind::Unsupported(surface) => {
                return Err(format!(
                    "no auto-derivable blank for field `{field}` of `{owner}`: its type `{surface}` has no synthesisable default. Define a top-level `Codec {owner}` binding in the project (spa-split copies it into Shared)."
                ))
            }
        })
    }
}

/// The actionable error for a bare top-level union that `Codec.auto` cannot
/// decode across the wire.
fn bare_union_no_codec_msg(name: &str, surface: &str) -> String {
    format!(
        "no codec for a field of type `{surface}` — `Codec.auto` cannot DECODE a bare data-carrying union across the Sky.Spa wire (only a record's FIELDS decode an ADT). Define a top-level `Codec {name}` binding in the project (spa-split copies it into Shared), or carry the value inside a record."
    )
}

/// Render a record literal from `field = value` parts, one field per line, in the
/// `{ … , … }` layout `sky fmt` produces.
fn render_record_literal(parts: &[String]) -> String {
    if parts.is_empty() {
        return "{}".to_string();
    }
    let mut out = String::new();
    for (i, p) in parts.iter().enumerate() {
        let lead = if i == 0 { "{ " } else { ", " };
        out.push_str(&format!("{lead}{p}\n            "));
    }
    // Trim the trailing indentation before the closing brace.
    let out = out.trim_end();
    format!("{out}\n            }}")
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
    /// Build-time WARNINGS the author must see (printed prominently by the CLI).
    /// Currently: a model field whose type `Codec.auto` cannot round-trip through
    /// the SSR model embed, caught at build time rather than as a runtime
    /// console.error + silent fall-back to `init`.
    pub warnings: Vec<String>,
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
/// A model field's type that `Codec.auto` cannot faithfully round-trip through
/// the Sky.Spa SSR model embed, returning the surface type label to name in the
/// diagnostic (or `None` when the field round-trips).
///
/// The SSR first paint embeds `Codec.toJson (Codec.auto model)` and the client
/// decodes it with the symmetric `Codec.fromJson (Codec.auto blank)`. Most
/// stdlib shapes round-trip: the runtime encoder/decoder (runtime-go
/// codec_auto.go) has arms for `List`, `Maybe`, `Dict`, `Set`, `Decimal`,
/// `Money`, and general data-carrying ADTs, each covered by codec_auto_test.go —
/// so they are deliberately NOT flagged (flagging a type that works would be a
/// false positive on a shipping app).
///
/// The type that provably CANNOT survive the round-trip is the opaque `Secret`:
/// `rt.Secret` carries an unexported field and a `MarshalJSON` that redacts
/// itself in every JSON path, so an embedded secret comes back as a redacted /
/// empty value — a silent SSR-embed vs client-decode divergence — and, worse, a
/// real secret must never be embedded in the first-paint HTML the client can
/// read. Detected on the resolved type's nominal tail (the same tail-segment
/// convention `field_ty_codec` uses), with a surface-name fallback.
fn codec_auto_unencodable(f: &ModelFieldTy) -> Option<(String, String)> {
    // The nominal tail, e.g. `Sky.Core.Set.Set` -> "Set". Same tail convention
    // `field_ty_codec` uses.
    fn tail(name: &str) -> &str {
        name.rsplit('.').next().unwrap_or(name)
    }
    // Find a provably un-round-trippable nominal ANYWHERE in the type — top
    // level or nested inside List / Maybe / Dict / Tuple / a record field. Two
    // shapes qualify, and only these two (Money / Decimal / Dict / Maybe / List /
    // Time.Posix / data-carrying ADTs all round-trip, covered by
    // codec_auto_test.go — flagging one would be a false positive on a shipping
    // app):
    //   * "Secret" — rt.Secret redacts itself in every JSON path.
    //   * "Set" — it has no goty arm, so a Set field erases to Go `any`; the
    //     encode side emits an array but `Codec.auto`'s decode has no `Set` arm
    //     and errors ("cannot decode kind interface").
    fn scan(t: &ty::Ty) -> Option<&'static str> {
        match t {
            ty::Ty::App(name, args) => {
                match tail(name.as_str()) {
                    "Secret" => return Some("Secret"),
                    "Set" => return Some("Set"),
                    _ => {}
                }
                args.iter().find_map(scan)
            }
            ty::Ty::Record(fields, _) => fields.iter().find_map(|(_, t)| scan(t)),
            ty::Ty::Tuple(items) => items.iter().find_map(scan),
            ty::Ty::Fun(a, b) => scan(a).or_else(|| scan(b)),
            _ => None,
        }
    }
    let why = |kind: &str| -> String {
        if kind == "Secret" {
            "`Secret` redacts itself in every JSON path (rt.Secret.MarshalJSON), so the first paint would embed a redacted/empty value and the client would decode it back wrong — a silent SSR-embed vs client-decode divergence — and a secret must never be embedded in client-readable HTML. Keep the secret server-side (out of the client model), or model a non-secret handle the client can safely carry.".to_string()
        } else {
            "`Set a` has no Go representation of its own — it erases to `any`. The SSR embed encodes it as a JSON array, but `Codec.auto`'s client decode has no `Set` arm and fails (\"cannot decode kind interface\"), so the first paint falls back to `init` (empty) while Sky.Live renders the Set. Model the field as a `List a` (dedup in `update`), which round-trips.".to_string()
        }
    };
    if let Some(t) = &f.ty {
        if let Some(kind) = scan(t) {
            return Some((f.ty_name.clone(), why(kind)));
        }
    }
    // Resolved type unavailable — fall back to the surface rendering. Tokenise
    // it (a rendered application is `Set String`, `Maybe Secret`, `{ k : Secret }`)
    // and match any token's nominal tail, so a nested occurrence is still caught.
    if f.ty.is_none() {
        for tok in f
            .ty_name
            .split(|c: char| !c.is_alphanumeric() && c != '.' && c != '_')
        {
            match tail(tok) {
                "Secret" => return Some((f.ty_name.clone(), why("Secret"))),
                "Set" => return Some((f.ty_name.clone(), why("Set"))),
                _ => {}
            }
        }
    }
    None
}

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
/// PATTERN-2 (client-result perform) descriptor for one server root branch. The
/// root's RPC answers with the task RESULT (`result_ty`, a `Result Error T`); the
/// frontend dispatches `result_msg result` client-side.
#[derive(Clone)]
struct ClientResultInfo {
    /// The CLIENT result Msg the frontend dispatches with the whole result value.
    result_msg: String,
    /// The task's result type (`Result Error T`) — the RPC response payload,
    /// carried in the root's `<Root>Resp` record as a single `result` field.
    result_ty: ty::Ty,
}

fn build_wire(
    name: &str,
    io: &BranchIo,
    msg_arg_tys: &[ModelFieldTy],
    model_fields: &[ModelFieldTy],
    resolver: &mut CodecResolver,
    // PATTERN-2: when this branch is a client-result root, its RESPONSE is NOT the
    // write-set but a single `result : Result Error T` field (the task result the
    // frontend dispatches into `update`). The request is unchanged (read-set + Msg
    // args — the task's own inputs).
    client_result: Option<&ClientResultInfo>,
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
    let mut resp: Vec<ModelFieldTy> = if let Some(cr) = client_result {
        // PATTERN-2: the response is the task RESULT, carried as a single
        // `result : Result Error T` field. The write-set is NOT the response —
        // the result Msg's client arm applies the model update in the wasm client.
        vec![ModelFieldTy {
            name: "result".to_string(),
            ty_name: String::new(),
            codec: None,
            ty: Some(cr.result_ty.clone()),
        }]
    } else if io.writes_whole_model {
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
/// Render the synthesised auto-derived record codecs (§14 #2, option B): for each
/// record `N` the resolver auto-derived, a nominally-annotated blank
/// (`blank<N>_ : <N>`) plus `auto<N>Codec_ = Codec.auto blank<N>_`. The
/// annotation to the NOMINAL `<N>` is REQUIRED — an inline unannotated literal
/// types structurally with erased element types, so `Codec.auto` would reflect
/// `kind interface` and drop nested collections on decode (the `spaModelBlank_`
/// lesson). `<N>` itself is copied / imported into `Shared` via the type-copy
/// seed.
fn render_auto_codec_defs(auto_records: &BTreeMap<String, String>) -> String {
    let mut out = String::new();
    for (name, blank) in auto_records {
        out.push_str(&format!(
            "-- Auto-derived codec for the plain record `{name}` (no user `Codec {name}`).\n\
             blank{name}_ : {name}\n\
             blank{name}_ =\n    \
             {blank}\n\n\n\
             auto{name}Codec_ : Codec {name}\n\
             auto{name}Codec_ =\n    \
             Codec.auto blank{name}_\n\n\n"
        ));
    }
    out
}

fn gen_shared(
    wires: &[Wire],
    imports: &[String],
    copied_decls: &str,
    auto_codec_defs: &str,
    copied_exposing: &[String],
    session_proj: &[SessionProjField],
) -> String {
    let mut exposing: Vec<String> = copied_exposing.to_vec();
    let mut bodies = String::new();
    // STATELESS SIGNED SESSION: a dedicated exported codec per identity field, so
    // the backend can name `spaSessionCodec<Field>_` to sign the value into the
    // `sky_sid` cookie and verify it back. It reuses the SAME resolved codec
    // expression the wire records use (an auto-derived or user codec, resolved in
    // `generate`), which lives in this module — never a hand-rolled rt.Coerce.
    for p in session_proj {
        let cname = session_codec_name(&p.name);
        exposing.push(cname.clone());
        bodies.push_str(&format!(
            "-- Stateless signed-session codec for identity field `{0}` — the backend\n\
             -- signs the value into `sky_sid` and verifies it back through this codec\n\
             -- (never trusting the wire model).\n\
             {1} : Codec {2}\n\
             {1} =\n    {3}\n\n\n",
            p.name,
            cname,
            wrap_arg(&p.surface),
            p.codec
        ));
    }
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
    let auto_block = if auto_codec_defs.trim().is_empty() {
        String::new()
    } else {
        format!("{}\n\n\n", auto_codec_defs.trim_end())
    };
    format!(
        "-- | Shared — the ONE RPC wire contract compiled into BOTH the Sky.Spa wasm\n\
         -- client and the native Sky.Http.Server backend. Generated by `sky spa-split`.\n\
         -- One type, one codec, one wire shape: change a field and BOTH stop compiling.\n\
         {module_header}\n\n\
         {import_block}\n\n\n\
         {copied_block}{auto_block}{bodies}"
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

/// Remove `names` from an import line's `exposing (...)` list. Those names are
/// provided by the generated `import Shared exposing (..)` (the copied types +
/// codecs), so keeping them on the original import — whose source module is
/// still copied verbatim into the backend — would be an ambiguous double-import
/// (E-level). Handles the single- and multi-line parenthesised list; leaves an
/// `exposing (..)` import and an import with no `exposing` clause unchanged
/// (nothing to strip per name). If every exposed name is stripped, the whole
/// `exposing (...)` clause is dropped, leaving a bare (possibly aliased) import.
fn strip_names_from_import_exposing(text: &str, names: &HashSet<String>) -> String {
    if names.is_empty() {
        return text.to_string();
    }
    let Some(exp_at) = text.find("exposing") else {
        return text.to_string();
    };
    let after = &text[exp_at + "exposing".len()..];
    let Some(open_rel) = after.find('(') else {
        return text.to_string();
    };
    let open = exp_at + "exposing".len() + open_rel;
    let bytes = text.as_bytes();
    let mut depth = 0i32;
    let mut close = None;
    for i in open..text.len() {
        match bytes[i] {
            b'(' => depth += 1,
            b')' => {
                depth -= 1;
                if depth == 0 {
                    close = Some(i);
                    break;
                }
            }
            _ => {}
        }
    }
    let Some(close) = close else {
        return text.to_string();
    };
    let inner = &text[open + 1..close];
    if inner.trim() == ".." {
        return text.to_string();
    }
    let kept: Vec<String> = inner
        .split(',')
        .map(|s| s.trim())
        .filter(|s| !s.is_empty())
        .filter(|item| {
            // A union member is exposed as `Foo(..)`; its bare name is `Foo`.
            let bare = item.split('(').next().unwrap_or(item).trim();
            !names.contains(bare)
        })
        .map(|s| s.to_string())
        .collect();
    let head = text[..exp_at].trim_end();
    let tail = &text[close + 1..];
    if kept.is_empty() {
        format!("{head}{tail}")
    } else {
        format!("{head} exposing ({}){tail}", kept.join(", "))
    }
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
    // The app's declared static mount `(dir, url-prefix)`, supplied by the caller
    // when the entry `generate` sees has DROPPED the declaration — the `--target
    // web:app` synth entry, whose `App.withConfig (WebConfig { static })` is gone.
    // `None` → `generate` reads it from its own entry + `sky.toml` (the direct
    // `sky spa-split` path, whose entry still carries the declaration).
    static_mount_override: Option<(String, String)>,
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

    // G5: `init`'s returned MODEL embeds a server read the wasm client cannot
    // reproduce. Emitting a frontend would either reference the backend-only read
    // (a leak / a build failure) or silently drop the data — so REFUSE with the
    // actionable fix (defer the read to `init`'s command + a `Got<Field>` arm).
    // A server read in `init`'s COMMAND is the supported deferred pattern and is
    // NOT flagged here (the analysis isolates the model, never the command).
    if !report.init_model_server_reads.is_empty() {
        return Err(format!(
            "cannot auto-split: `init`'s returned model embeds server read(s) the wasm client cannot reproduce: {}. \
Move the read out of the initial model — return the empty/default model from `init` and put the read in its COMMAND \
(`Cmd.perform <task> Got<Field>`), then fold the loaded value into the model via a `Got<Field>` update arm. \
The command runs server-side during SSR and the client hydrates from it; a read baked into the model has no client value.",
            report.init_model_server_reads.join("; ")
        ));
    }

    let mut notes: Vec<String> = Vec::new();
    let mut warnings: Vec<String> = Vec::new();

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
    // A module is TAINTED when it has ANY server-tainted top-level binding.
    let module_has_tainted = |mid: ModuleId, db: &SkyDatabase| -> bool {
        let mname = db.module_name(mid).to_string();
        tainted_by_module
            .get(&mname)
            .map(|s| !s.is_empty())
            .unwrap_or(false)
    };
    // A module with NO tainted binding is a PURE sibling (copied to both trees
    // verbatim). A module WITH a tainted binding is routed PER-BINDING (§17,
    // GAP-2): its FULL body stays backend, and — when the frontend actually needs
    // one of its pure defs, or it declares `update` / the `Msg` union — a client
    // SUBSET of just its non-tainted defs is emitted into the frontend. A tainted
    // module whose pure defs the frontend never reaches keeps its whole body
    // backend-only (no frontend copy), exactly as before.
    let mut tainted_mods: Vec<ModuleId> = Vec::new();
    let mut pure_sibling_mods: Vec<ModuleId> = Vec::new();
    for m in check_ids.iter().copied().filter(|m| *m != entry) {
        if module_has_tainted(m, &db) {
            tainted_mods.push(m);
        } else {
            pure_sibling_mods.push(m);
        }
    }

    // GAP-1: the module that DECLARES `update` — the entry, OR a sibling module
    // (factored out — the sky-lang.org shape). Resolved cross-module by the
    // report; its partitioned `update` is regenerated in ITS OWN frontend copy,
    // never assumed to live in the entry.
    let update_module: ModuleId = report
        .update_module_name
        .as_deref()
        .and_then(|n| db.module_by_name(n))
        .filter(|m| check_ids.contains(m))
        .unwrap_or(entry);
    let update_in_entry = update_module == entry;

    // GAP-2: which tainted modules' PURE defs does the frontend actually reach? A
    // frontend-retained root (an entry non-tainted def, or any pure-sibling def)
    // that references a non-tainted def living in a tainted module means that
    // module must emit a client subset. This is the exact condition the build
    // used to REFUSE on; it now drives per-binding emission instead. `update` is
    // never in the tainted set, so the entry's `main` referencing a SIBLING
    // `update` flags that sibling here too.
    let mut tainted_pure_defs: HashMap<DefId, ModuleId> = HashMap::new();
    for m in &tainted_mods {
        let mname = db.module_name(*m).to_string();
        let tainted_here = tainted_by_module.get(&mname);
        for td in &db.resolve(*m).top_defs {
            let is_tainted = tainted_here
                .map(|s| s.contains(td.name.as_str()))
                .unwrap_or(false);
            if !is_tainted {
                tainted_pure_defs.insert(td.def, *m);
            }
        }
    }
    let mut referenced_subset_mods: HashSet<ModuleId> = HashSet::new();
    if !tainted_pure_defs.is_empty() {
        let entry_tainted = tainted_by_module.get(&entry_name);
        let mut roots: Vec<(ModuleId, DefId)> = Vec::new();
        for td in &db.resolve(entry).top_defs {
            let is_tainted = entry_tainted
                .map(|s| s.contains(td.name.as_str()))
                .unwrap_or(false);
            if !is_tainted {
                roots.push((entry, td.def));
            }
        }
        for m in &pure_sibling_mods {
            for td in &db.resolve(*m).top_defs {
                roots.push((*m, td.def));
            }
        }
        for (mid, def) in &roots {
            for c in spa_partition::body_def_callees(&db, *mid, *def) {
                if let Some(owner) = tainted_pure_defs.get(&c) {
                    referenced_subset_mods.insert(*owner);
                }
            }
        }
    }

    // SERVER-INTERNAL Msgs (server-internal effect chaining) are dispatched ONLY
    // from a server `Cmd.perform` and settle inside the triggering branch's RPC —
    // they get NO /_rpc route, so they are excluded from the wire `server` set
    // even when they are server-CLASSIFIED (their own arm reaches an effect, e.g.
    // `EmailSent` logging its result). Their client arm + Msg-union constructor
    // are pruned from the frontend below.
    let server_internal_names: HashSet<String> = report.server_internal.iter().cloned().collect();

    // SERVER branches, keyed by ctor name, with their RPC I/O + typed Msg args.
    let mut server: Vec<(String, BranchIo)> = Vec::new();
    let mut server_args: HashMap<String, Vec<ModelFieldTy>> = HashMap::new();
    let mut client_names: Vec<String> = Vec::new();
    for b in &report.branches {
        let name = ctor_name(&b.msg).to_string();
        if server_internal_names.contains(&name) {
            // Server-internal — settled server-side in its trigger's RPC; no wire
            // route, no client arm. Deduped across its (Ok/Err) arms.
            continue;
        }
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
    // A Msg may have several arms (Ok/Err); keep ONE wire entry per ctor (a Msg
    // is one RPC route). Robust to non-consecutive arms.
    {
        let mut seen: HashSet<String> = HashSet::new();
        server.retain(|(n, _)| seen.insert(n.clone()));
        let mut seen_c: HashSet<String> = HashSet::new();
        client_names.retain(|n| seen_c.insert(n.clone()));
    }
    if server.is_empty() {
        notes.push("no SERVER branches — the frontend is fully client-local and the backend only serves static assets.".into());
    }

    // ---- GAP-A: resolve the module that DECLARES the `Msg` union ----
    // The generated frontend `update` references the `Applied<Msg>` RPC-response
    // variants, so they must be injected into the `Msg` union WHEREVER it is
    // declared. When `Msg` lives in a pure sibling (the sky-lang.org shape —
    // `Msg` in `State.sky`) the variants are spliced into that module's FRONTEND
    // copy (the entry-source injection in `gen_frontend` only fires for a `Msg`
    // in the entry); when it lives in a MIXED module (the compose case — `Msg`
    // next to `update` in a sibling), the injection is composed into that
    // module's client subset (render_module_client_subset). Identify the module
    // by the union whose variants include the SERVER branch constructors — robust
    // to the type's name and to a same-named `Msg` in an unrelated module (an
    // embedded example). Searched across EVERY non-entry module (pure siblings
    // AND tainted/mixed modules) so a `Msg` beside `update` in a mixed module is
    // found. Only relevant when there are server branches.
    let msg_module: Option<ModuleId> = if server.is_empty() {
        None
    } else {
        let want: HashSet<&str> = server.iter().map(|(n, _)| n.as_str()).collect();
        check_ids
            .iter()
            .copied()
            .filter(|m| *m != entry)
            .find(|m| {
                db.module_parse(*m).tree().decls().any(|d| {
                    matches!(decl_kind(&d), DeclKind::Union)
                        && union_variant_names(&d).iter().any(|v| want.contains(v.as_str()))
                })
            })
    };

    // ---- finalise the per-binding routing (GAP-1 / GAP-2 / §17) ----
    // A tainted module emits a frontend SUBSET when the frontend reaches one of
    // its pure defs, OR it declares `update` (regenerated there — GAP-1), OR it
    // declares the `Msg` union (Applied variants injected there — GAP-A).
    // Otherwise its whole body is backend-only (no frontend copy). The backend
    // copy of EVERY tainted module stays the FULL module.
    let mut subset_mods: Vec<ModuleId> = Vec::new();
    let mut no_frontend_mods: Vec<ModuleId> = Vec::new();
    for m in tainted_mods.iter().copied() {
        let is_update = m == update_module;
        let is_msg = Some(m) == msg_module;
        if referenced_subset_mods.contains(&m) || is_update || is_msg {
            subset_mods.push(m);
        } else {
            no_frontend_mods.push(m);
        }
    }
    // Modules with NO frontend copy — the security spine drops any import of one.
    let no_frontend_names: HashSet<String> = no_frontend_mods
        .iter()
        .map(|m| db.module_name(*m).to_string())
        .collect();
    if !tainted_mods.is_empty() || !pure_sibling_mods.is_empty() {
        let pure: Vec<String> = pure_sibling_mods.iter().map(|m| db.module_name(*m).to_string()).collect();
        let subset: Vec<String> = subset_mods.iter().map(|m| db.module_name(*m).to_string()).collect();
        let back: Vec<String> = no_frontend_mods.iter().map(|m| db.module_name(*m).to_string()).collect();
        notes.push(format!(
            "multi-module split (per-binding): pure module(s) {pure:?} copied to BOTH trees; server-only module(s) {back:?} routed backend-only; mixed module(s) {subset:?} split per-binding (client-safe bindings copied to the frontend; server bindings kept backend-only)."
        ));
    }

    let parse = db.module_parse(entry);
    let src = parse.syntax().text().to_string();
    let file = parse.tree();

    let imports = collect_imports(&file, &src);

    // Tainted binding names → excluded from the frontend.
    let tainted_names: Vec<String> = report.tainted.iter().map(|t| t.name.clone()).collect();

    // ---- resolve the wire codecs (§14 #2) ----
    // Registry of the project's own `Codec <T>` bindings, scanned across the
    // entry module, every PURE sibling module (a codec may live in `Domain`) AND
    // every MIXED/tainted module. A `Codec <T>` binding is a zero-arg PURE value
    // even when it lives beside a server effect (the common `basketItemCodec`
    // next to `Db.query` shape), so it is eligible regardless of its module's
    // taint. Each binding remembers its module so the copy-vs-import decision
    // below can COPY an entry- or mixed-module codec into `Shared` but IMPORT a
    // pure-sibling one.
    let pure_sibling_set: HashSet<ModuleId> = pure_sibling_mods.iter().copied().collect();
    let mut codec_scan_mods: Vec<ModuleId> = vec![entry];
    codec_scan_mods.extend(pure_sibling_mods.iter().copied());
    codec_scan_mods.extend(tainted_mods.iter().copied());
    let registry = build_codec_registry(&db, &codec_scan_mods);
    let shapes = build_project_shapes(&db, &codec_scan_mods);
    // PATTERN-2 (client-result perform): map each server root to its result Msg +
    // the task's result type (read from the result Msg's union-variant argument).
    // The map is the single source of truth consulted by build_wire / gen_backend
    // / gen_frontend; a result type we cannot recover leaves the root a plain wire
    // branch (fail closed, today's behaviour).
    let client_result_map = build_client_result_map(&db, &check_ids, &report.client_result);
    let mut resolver = CodecResolver::new(&registry, &shapes);
    let mut wires: Vec<Wire> = Vec::new();
    for (name, io) in &server {
        let args = server_args.get(name).cloned().unwrap_or_default();
        wires.push(build_wire(
            name,
            io,
            &args,
            &report.model_fields,
            &mut resolver,
            client_result_map.get(name),
        )?);
    }
    // STATELESS SIGNED SESSION (security): the identity projection the backend
    // signs into an httpOnly `sky_sid` cookie and verifies on every RPC + SSR. A
    // model field whose type is nominally `Session` / `Maybe Session` AND which
    // some server branch writes (writes_whole_model ⇒ every field eligible), so
    // there is a trusted server-side value to sign. EMPTY → the app has no
    // server-trusted session and NOTHING is emitted (behaviour unchanged). Every
    // projection field is written by some server branch, so its type already rode
    // a Resp wire above and `resolve` is idempotent here — computed before
    // `auto_codec_defs` / `seed_ty` so any residual auto-record still reaches
    // `Shared`.
    let session_projection: Vec<SessionProjField> = {
        let any_writes_whole = server.iter().any(|(_, io)| io.writes_whole_model);
        let written: HashSet<String> = server
            .iter()
            .flat_map(|(_, io)| io.write_fields.iter().cloned())
            .collect();
        let mut out: Vec<SessionProjField> = Vec::new();
        for f in &report.model_fields {
            let Some(t) = &f.ty else { continue };
            if !is_session_identity_ty(t, &shapes) {
                continue;
            }
            if !(any_writes_whole || written.contains(&f.name)) {
                continue;
            }
            let r = resolver.resolve(t).map_err(|e| {
                format!(
                    "stateless signed session: cannot resolve a codec for identity field `{}`: {e}",
                    f.name
                )
            })?;
            out.push(SessionProjField {
                name: f.name.clone(),
                codec: r.codec,
                surface: r.surface,
            });
        }
        out
    };

    // The synthesised blank + `Codec.auto` bodies for every auto-derived record,
    // rendered once here and emitted into `Shared` after the copied types.
    let auto_codec_defs = render_auto_codec_defs(&resolver.auto_records);

    // The nominal type names the wire field types drag in (the type-copy seed).
    let mut seed_ty: BTreeSet<String> = BTreeSet::new();
    for w in &wires {
        for f in w.req_fields.iter().chain(w.resp_fields.iter()) {
            if let Some(t) = &f.ty {
                collect_ty_names(t, &mut seed_ty);
            }
        }
    }
    // An auto-derived record is often a STRUCTURAL row in the wire field's solved
    // type (a record alias the solver expanded), so its nominal name never
    // appears in `collect_ty_names`. Seed each explicitly so `<N>` — and, via the
    // transitive type-copy closure, every nested type it mentions — reaches
    // `Shared`.
    for n in resolver.auto_records.keys() {
        seed_ty.insert(n.clone());
    }

    // ---- copy-vs-import: which modules feed `Shared` by COPY (§14 #2) ----
    // A codec binding a wire references is COPIED into `Shared` when it lives in
    // the ENTRY or in a MIXED (server-tainted) module — the latter because
    // importing a tainted module into `Shared` would drag its server effect into
    // BOTH trees (`Shared` compiles into the wasm frontend too). A codec in a
    // PURE sibling module is IMPORTED instead (that module is copied whole to
    // both trees). Wire-field TYPES declared in a mixed module are copied for the
    // same reason; those in a pure sibling are imported.
    let mut copy_mods: BTreeSet<ModuleId> = BTreeSet::new();
    copy_mods.insert(entry);
    for b in &registry {
        if resolver.needed.contains(&b.name)
            && b.module != entry
            && !pure_sibling_set.contains(&b.module)
        {
            copy_mods.insert(b.module);
        }
    }
    // A wire-field type declared in a MIXED module must be copied from there too
    // (it cannot be imported without leaking the module's effects).
    for m in tainted_mods.iter().copied() {
        let mparse = db.module_parse(m);
        let mtypes = project_type_decls(&mparse.tree());
        if seed_ty.iter().any(|n| mtypes.contains_key(n)) {
            copy_mods.insert(m);
        }
    }

    // GAP-A cycle break — the co-located `Msg` + wire-types shape. When the `Msg`
    // union lives in a PURE sibling module that ALSO declares a wire type/codec
    // `Shared` references, injecting the `Applied<Msg>` RPC variants makes that
    // module `import Shared`. If `Shared` then IMPORTED the module back (it would
    // land in `needed_siblings`) the two-way import is a cycle (E1010 — the
    // refusal this fix deletes). Resolve it by MOVING ownership of those wire
    // declarations INTO `Shared` (add the module to `copy_mods`): `Shared`
    // declares its own copy and never imports the Msg module. A wire record is a
    // structural `type alias`, so `Shared`'s copy and the copy left in the Msg
    // module unify — the model field and the RPC response field reconcile without
    // a nominal single-definition. (A NOMINAL wire type — a `union` — cannot be
    // duplicated soundly; that case is caught below and refused, since it is the
    // genuinely-unavoidable residual cycle.)
    let msg_is_pure_sibling = msg_module
        .map(|m| pure_sibling_set.contains(&m))
        .unwrap_or(false);
    if let (Some(mm), true) = (msg_module, msg_is_pure_sibling) {
        copy_mods.insert(mm);
    }

    // The pure transitive value closure of every referenced codec, grouped by the
    // module that DECLARES it (only defs living in `copy_mods` are copied). Fails
    // closed if a referenced codec's closure reaches a server-tainted def — that
    // codec would drag an effect into `Shared`, so it is not eligible.
    let copied_values_by_mod =
        compute_value_copy(&db, &copy_mods, &registry, &resolver.needed, &tainted_by_module)?;

    // Assemble the copied declarations + `exposing` list per source module, then
    // concatenate. Each module contributes its own value + type closure, rendered
    // verbatim from its own source (a mixed module's `Item` / `itemCodec` are
    // copied out of `Data.sky`, not the entry).
    let mut copied_names: HashSet<String> = HashSet::new();
    let mut copied_decls = String::new();
    let mut copied_exposing: Vec<String> = Vec::new();
    // Every copied name → the source module NAME that declared it. A consumer that
    // still reaches the name through a surviving `import <src> exposing (..)` reads
    // it from there; only a consumer that lost it (declared-and-stripped, or an
    // explicit import that was stripped) needs it re-imported from `Shared`. Used
    // by `shared_expose_clause` to keep a copied structural type single-sourced per
    // consumer (never imported from BOTH `Shared` and its origin — an ambiguity).
    let mut copied_name_source: BTreeMap<String, String> = BTreeMap::new();
    // The moved NOMINAL unions: type name → the words that name it in source (the
    // type name plus every constructor). A `union` is a NOMINAL type — two
    // same-named unions in different modules are distinct `DefId`s that never
    // unify — so it cannot be duplicated copy-and-leave the way a structural
    // record `type alias` can. `Shared` therefore OWNS each moved union as its
    // SINGLE definition: every other module copy strips its declaration and
    // imports the name (with `(..)` for the constructors) from `Shared`. The
    // `Msg` union itself is NEVER moved (it is the `Applied<Msg>` inject target,
    // stays in its module, and is not a wire field), so it is excluded below.
    let msg_union_name: Option<String> = msg_module.and_then(|m| {
        let want: HashSet<&str> = server.iter().map(|(n, _)| n.as_str()).collect();
        db.module_parse(m)
            .tree()
            .decls()
            .find(|d| {
                matches!(decl_kind(d), DeclKind::Union)
                    && union_variant_names(d).iter().any(|v| want.contains(v.as_str()))
            })
            .and_then(|d| decl_name(&d))
    });
    let mut moved_unions: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for m in copy_mods.iter().copied() {
        let mparse = db.module_parse(m);
        let mfile = mparse.tree();
        let msrc = mparse.syntax().text().to_string();
        let mtypes = project_type_decls(&mfile);
        let mut values: BTreeSet<String> = copied_values_by_mod
            .get(&m)
            .cloned()
            .unwrap_or_default();
        // A record-alias constructor (`Codec.object Item`) resolves to a def named
        // like the type — keep those in the TYPE-copy set, never the value set.
        values.retain(|n| !mtypes.contains_key(n));
        let types = compute_type_copy(&mfile, &mtypes, &seed_ty, &values);
        // A moved NOMINAL type (a `union`) in this module's type-copy set cannot be
        // duplicated: two same-named unions in different modules never unify. So
        // record it as a MOVED UNION — `Shared` owns the single definition (it is
        // in `copied_names`, rendered by `render_copied_decls`), every other copy
        // strips its declaration and imports `Name(..)` from `Shared`. The `Msg`
        // union is never in this set (it is not a wire field), and is excluded by
        // name defensively. Structural record `type alias`es keep copy-and-leave.
        for n in &types {
            let is_union =
                mtypes.get(n).map(|d| decl_kind(d) == DeclKind::Union).unwrap_or(false);
            if is_union && Some(n) != msg_union_name.as_ref() {
                let mut words = vec![n.clone()];
                if let Some(d) = mtypes.get(n) {
                    words.extend(union_variant_names(d));
                }
                moved_unions.insert(n.clone(), words);
            }
        }
        let mut names: HashSet<String> = HashSet::new();
        names.extend(values.iter().cloned());
        names.extend(types.iter().cloned());
        if names.is_empty() {
            continue;
        }
        copied_decls.push_str(&render_copied_decls(&mfile, &msrc, &names));
        for e in copied_exposing_list(&mtypes, &types, &values) {
            copied_exposing.push(e);
        }
        let mname = db.module_name(m).to_string();
        for n in &names {
            copied_name_source.insert(n.clone(), mname.clone());
        }
        copied_names.extend(names);
    }
    // De-dup the exposing list (a name never appears twice across modules).
    let mut seen_exp: HashSet<String> = HashSet::new();
    copied_exposing.retain(|e| seen_exp.insert(e.clone()));

    // ---- pure sibling modules the wire references (Shared imports them) ----
    // A referenced codec or a wire-field type DECLARED in a PURE sibling module
    // is NOT copied into Shared (the module is copied whole to both trees);
    // Shared imports the module instead. A codec in a MIXED/tainted module is
    // COPIED above, never imported (importing it would leak its server effects
    // into the wasm frontend).
    let mut needed_siblings: BTreeSet<String> = BTreeSet::new();
    for b in &registry {
        // A module already in `copy_mods` (the co-located Msg module) is COPIED
        // into `Shared`, never imported — importing it would re-open the cycle.
        if resolver.needed.contains(&b.name)
            && pure_sibling_set.contains(&b.module)
            && !copy_mods.contains(&b.module)
        {
            needed_siblings.insert(db.module_name(b.module).to_string());
        }
    }
    // Wire-field types declared in a pure sibling module (e.g. `Todo` in `Domain`).
    for m in &pure_sibling_mods {
        if copy_mods.contains(m) {
            continue; // owned by `Shared` (copied), not imported
        }
        let mname = db.module_name(*m).to_string();
        let mparse = db.module_parse(*m);
        let mfile = mparse.tree();
        let mtypes = project_type_decls(&mfile);
        if seed_ty.iter().any(|n| mtypes.contains_key(n)) {
            needed_siblings.insert(mname);
        }
    }
    // Residual (the one genuinely-unclosable shape). A pure sibling that `Shared`
    // IMPORTS (a `needed_sibling` — it provides a wire type/codec that is not
    // copied) yet which ALSO references a moved union would need to import
    // `Shared` back for that union's single definition — a two-way import cycle
    // (E1010). Refuse precisely rather than emit a cyclic tree. (The common
    // shape — a `Msg` module in `copy_mods`, never a `needed_sibling` — does not
    // hit this; only a module that is BOTH a wire-type provider AND a moved-union
    // consumer does.)
    if !moved_unions.is_empty() {
        for m in &pure_sibling_mods {
            let mname = db.module_name(*m).to_string();
            if !needed_siblings.contains(&mname) {
                continue;
            }
            let msrc = db.module_parse(*m).syntax().text().to_string();
            if let Some((u, _)) = moved_unions
                .iter()
                .find(|(_, words)| words.iter().any(|w| module_mentions_word(&msrc, w)))
            {
                return Err(format!(
                    "cannot auto-split: module `{mname}` provides a wire type/codec that `Shared` imports, and it ALSO references the union `{u}` that `Shared` must OWN — so `{mname}` would have to import `Shared` back, forming an import cycle (E1010). Move `{u}` (and any type that references it) into a module `Shared` does not import, or move the wire type/codec out of `{mname}`."
                ));
            }
        }
    }
    // EVERY non-entry project module — Shared drops the entry's import of any of
    // them (a needed wire-type sibling is re-added with a canonical `exposing
    // (..)`). This MUST include the tainted/subset modules, not only the pure
    // siblings: a subset module (e.g. a sibling `update`) now imports `Shared`
    // for the wire codecs, so if `Shared` also carried the entry's `import
    // <that module>` it would form a `Shared` ↔ module cycle (E1010).
    let all_project_sibling_names: HashSet<String> = check_ids
        .iter()
        .copied()
        .filter(|m| *m != entry)
        .map(|m| db.module_name(m).to_string())
        .collect();
    let shared_imports = shared_import_lines(
        &imports,
        &needed_siblings,
        &no_frontend_names,
        &all_project_sibling_names,
    );

    // ---- `update`'s DECLARING module (GAP-1) ----
    // `update` may live in the entry OR a sibling. Read its param names +
    // annotation from the module that DECLARES it (the entry's `update_decl` is
    // absent when it is factored into a sibling, which would otherwise default the
    // params to `msg`/`model` and lose the real annotation). The model type name
    // still comes from the entry's `view`/`update` annotation.
    let upd_parse = db.module_parse(update_module);
    let upd_src = upd_parse.syntax().text().to_string();
    let upd_file = upd_parse.tree();
    let update_decl = upd_file
        .decls()
        .find(|d| decl_name(d).as_deref() == Some("update") && is_value_decl(d));
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
    let update_anno = decl_text_by(&upd_file, &upd_src, "update", DeclKind::TypeAnno)
        .unwrap_or_else(|| "update : Msg -> Model -> ( Model, Cmd Msg )".to_string());
    let model_ty = model_type_name(&file, &src).unwrap_or_else(|| "Model".to_string());

    // `App.withRpcError` presence — the synthesised `spaRpcError_` binding lives
    // in the ENTRY, so resolve the flag here (once) and thread it into both the
    // entry `update` regeneration and any sibling one (the lookup stays against
    // the entry even when `update` is regenerated in a sibling module).
    let has_rpc_error = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaRpcError_"));

    // ---- literal SSR route patterns, across EVERY project module (§4.1) ----
    // The per-route SSR registration needs each route's literal URL pattern so
    // the backend can mount `GET <pat>` (a more-specific mux entry than
    // `Server.static "/"`, so asset GETs still fall through to the file server).
    // The route table is often FACTORED INTO A SIBLING module and referenced as
    // `App.withRoutes Routes.routes` — the synthesised `spaRoutes_` binding is
    // then `List.concatMap App.spaRoute (Routes.routes)`, which carries no
    // literal of its own. Scanning only that binding text (the old behaviour)
    // therefore found nothing and left every non-root route falling to
    // `Server.static` → 404 (the sky-lang.org `/blog` gap). Scan the SOURCE of
    // every project module (entry + siblings) so a `Routes.sky` full of
    // `App.route "/blog" …` literals is seen. Deduped, source order; the bare
    // root `/` is dropped (the caller mounts it as the exact-root `GET /{$}`).
    let mut ssr_route_patterns: Vec<String> = Vec::new();
    for m in &check_ids {
        let msrc = db.module_parse(*m).syntax().text().to_string();
        for pat in spa_ssr_route_patterns(&msrc) {
            if !ssr_route_patterns.contains(&pat) {
                ssr_route_patterns.push(pat);
            }
        }
    }

    // ---- FINDING A: dedupe page SSR mounts against `App.api` GET endpoints ----
    // A path that is BOTH an `App.route` PAGE route (mounted below as
    // `GET <path>` by the per-route SSR block) AND an `App.api "GET <path>"`
    // server endpoint (mounted via `App.apiServerRoute spaApiRoutes_`) would
    // register `GET <path>` on the Go 1.22 mux TWICE — `http.ServeMux` PANICS at
    // boot on the duplicate pattern (rt_server.go), crash-looping the backend.
    // The Live build tolerates this: its single `/` dispatcher resolves
    // api-before-page inside ONE handler, so it never makes a second
    // registration; the split, which mounts each route as its own mux entry, does.
    // PRECEDENCE — the API handler WINS: an explicit `App.api "GET <path>"` is a
    // deliberate server endpoint (the sky-lang.org `/admin/login` OAuth entry),
    // so the auto-derived SSR page mount for the SAME method+path is suppressed
    // and the request reaches the api handler. Dedupe on METHOD+PATH, not path:
    // the SSR mounts are all `GET`, so only a GET api endpoint on a page path
    // collides — an `App.api "POST <path>"` sharing a page path is a DISTINCT mux
    // pattern and is left to mount. Scanned across every project module (the api
    // table is often a sibling / entry `apiRoutes`), same as the page patterns.
    let mut api_get_paths: HashSet<String> = HashSet::new();
    for m in &check_ids {
        let msrc = db.module_parse(*m).syntax().text().to_string();
        for p in spa_api_get_paths(&msrc) {
            api_get_paths.insert(p);
        }
    }
    ssr_route_patterns.retain(|p| !api_get_paths.contains(p));

    // ---- resolve `init`'s DECLARING module for the GET-safe SSR scan (§4.2) ----
    // `init` may be factored into a SIBLING module (the sky-lang.org shape — its
    // `init` lives in `Model.sky`, not the entry). Resolve the config's `init`
    // field to its declaring module via the import graph and read THAT module's
    // `init` source for the curated GET-safe read decision. Scanning only the
    // entry (the old `app_init_src(file, src)`) missed a sibling `init`, so the
    // SSR settle was skipped and the first paint shipped an empty `#sky-model`.
    let init_def = spa_partition::find_config_field_def(&db, &check_ids, entry, "init");
    let init_mod = init_def
        .and_then(|d| db.def_loc(d).map(|l| l.module))
        .unwrap_or(entry);
    let init_in_entry = init_mod == entry;
    let init_src = {
        let iparse = db.module_parse(init_mod);
        let isrc = iparse.syntax().text().to_string();
        app_init_src(&iparse.tree(), &isrc)
    };

    // ---- GAP-2: the frontend init-command strip is init-MODULE-aware ----
    // The client keeps `init` but must NOT run its command when that command is a
    // curated GET-safe read reaching a server-tainted binding the frontend drops
    // (a `db` CAF): the server settles the read + embeds `#sky-model`, and the
    // client boots from that blob. This decision was previously computed from the
    // ENTRY source only (`gen_frontend`), so a sibling-module `init` (the
    // sky-lang.org shape — `init` in `Boot`/`Model`, not the entry) was copied
    // VERBATIM into the wasm frontend with its `Cmd.perform (Db.query db …)`,
    // leaving `Undefined name: db` + the server-only `Db.query` kernel. Compute
    // it here from the RESOLVED init module so a sibling init is stripped in its
    // own module copy (below), exactly as an entry init is stripped in
    // `gen_frontend`.
    let strip_init_cmd = init_cmd_is_get_safe(&init_src)
        && tainted_names.iter().any(|t| references_word(&init_src, t));
    // `init`'s PURE model expr — the client SSR model decoder is derived from it
    // (`Codec.fromJson (Codec.auto <model>)`). Read from whichever module
    // declares `init`, so the decoder is emitted for a sibling init too.
    let init_pure_model: Option<String> = if strip_init_cmd {
        let iparse = db.module_parse(init_mod);
        let isrc = iparse.syntax().text().to_string();
        iparse
            .tree()
            .decls()
            .find(|d| decl_name(d).as_deref() == Some("init") && is_value_decl(d))
            .and_then(|d| init_pure_model_expr(&isrc, &d))
    } else {
        None
    };

    // ---- write the three trees ----
    let shared_src = gen_shared(
        &wires,
        &shared_imports,
        &copied_decls,
        &auto_codec_defs,
        &copied_exposing,
        &session_projection,
    );
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
    // The generated wire names + a per-consumer `import Shared exposing (…)`
    // clause. The clause keeps a copied structural type single-sourced in each
    // consumer: the entry / a subset re-read their own stripped copies from
    // `Shared`, but never re-import a name they still get from a surviving
    // `exposing (..)` origin (which would be an ambiguous double-import — the
    // failure mode of the co-located Msg shape). The ENTRY strips its own copied
    // decls, so `strips_self = true`.
    let generated = generated_wire_names(&server);
    let entry_name = db.module_name(entry).to_string();
    let entry_shared_expose =
        shared_expose_clause(&src, &entry_name, &copied_name_source, &generated, &moved_unions, true);
    // STATELESS SIGNED SESSION: the BACKEND additionally imports the per-field
    // `spaSessionCodec<Field>_` bindings `Shared` exports (the frontend never
    // signs / verifies, so its clause is left unchanged — no unused import). When
    // the entry clause is the `exposing (..)` catch-all the exported codecs are
    // already in scope, so no injection is needed.
    let backend_shared_expose = if session_projection.is_empty()
        || entry_shared_expose.contains("exposing (..)")
    {
        entry_shared_expose.clone()
    } else {
        let extra: Vec<String> = session_projection
            .iter()
            .map(|p| session_codec_name(&p.name))
            .collect();
        let trimmed = entry_shared_expose.trim_end();
        let base = trimmed.strip_suffix(')').unwrap_or(trimmed);
        format!("{base}, {})", extra.join(", "))
    };
    // Server-internal effect chaining: the Msgs to DROP from the frontend and the
    // server branches whose RPC handler settles a `Cmd.perform` chain server-side.
    let server_internal: HashSet<String> = report.server_internal.iter().cloned().collect();
    let chaining_set: HashSet<String> = report.chaining_branches.iter().cloned().collect();
    // A server-internal Msg's arm is DROPPED from the client, so it is no longer
    // a client-local branch — drop it from the reported client set.
    client_names.retain(|n| !server_internal.contains(n));
    for w in &report.server_chain_warnings {
        warnings.push(w.clone());
    }
    if !report.chaining_branches.is_empty() {
        notes.push(format!(
            "server-internal effect chaining: branch(es) [{}] settle a Cmd.perform chain server-side; Msg(s) [{}] are server-internal (no /_rpc route, no Applied variant, no client arm).",
            report.chaining_branches.join(", "),
            report.server_internal.join(", "),
        ));
    }
    if !client_result_map.is_empty() {
        let pairs: Vec<String> = report
            .client_result
            .iter()
            .filter(|(root, _)| client_result_map.contains_key(root))
            .map(|(root, rm)| format!("{root} -> {rm}"))
            .collect();
        notes.push(format!(
            "client-result perform (pattern-2): branch(es) [{}] run a server task in their RPC and return its RESULT; the frontend dispatches the result Msg client-side (the result Msg stays a client arm, no /_rpc route, no Req).",
            pairs.join(", "),
        ));
    }
    let model_field_names: Vec<String> = report.model_fields.iter().map(|f| f.name.clone()).collect();
    // The static mount: the caller's override (the synth entry dropped the
    // declaration), else read from this entry + its `sky.toml`.
    let static_mount =
        static_mount_override.or_else(|| app_static_mount(&src, project_dir));
    let backend_src = gen_backend(&file, &src, &imports, &server, &report.model_fields, &copied_names, &backend_shared_expose, push_mode, broker_url, &ssr_route_patterns, &init_src, &chaining_set, &client_result_map, static_mount.as_ref(), &session_projection, &mut warnings)?;
    let frontend_src = gen_frontend(
        &file,
        &src,
        &imports,
        &server,
        &client_names,
        &tainted_names,
        &copied_names,
        &entry_shared_expose,
        &msg_param,
        &model_param,
        &update_anno,
        &model_ty,
        &no_frontend_names,
        strip_init_cmd,
        init_in_entry,
        init_pure_model.as_deref(),
        update_in_entry,
        has_rpc_error,
        &server_internal,
        &model_field_names,
        &client_result_map,
    )?;

    // Enforce the client-builder invariant: every synthesised `spa*_` wrapper the
    // client `Spa.config` / builder chain references MUST be defined in the
    // frontend. A server-tainted OPTIONAL step (`|> Spa.withHead spaHead_`) is
    // dropped from the client chain; a server-tainted MANDATORY `view = spaView_`
    // fails with a clear diagnostic instead of a dangling `E1001`.
    let entry_taint_reason: HashMap<String, String> = report
        .tainted
        .iter()
        .filter(|t| t.module == entry_name)
        .map(|t| (t.name.clone(), t.reason.clone()))
        .collect();
    let frontend_src = enforce_client_builder_invariant(&frontend_src, &entry_taint_reason, &mut notes)?;

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

    // (The GAP-A cycle guard that used to refuse here — "Move `Msg` into its own
    // module" — is DELETED. The co-located `Msg` + wire-types shape now resolves
    // by MOVING the wire declarations into `Shared` (copy_mods, above) so `Shared`
    // never imports the Msg module: there is no cycle left to guard. The one
    // genuinely-unavoidable residual — a NOMINAL union the Msg module must keep
    // for its `exposing (..)` consumers yet `Shared` must own — is refused at the
    // copy site, where the union is identified precisely.)

    // ---- copy the sibling project modules (§17) ----
    // PURE modules go into BOTH trees verbatim (with the `Applied<Msg>` inject /
    // init-strip for the entry-adjacent shapes). A TAINTED module always copies
    // its FULL body to the backend; whether it also emits a frontend copy is the
    // per-binding decision above:
    //   * subset_mods — a client SUBSET of its non-tainted defs (GAP-2), with the
    //     partitioned `update` regenerated (GAP-1) / the `Applied<Msg>` variants
    //     injected (GAP-A) when it declares them.
    //   * no_frontend_mods — backend only, never emitted into the wasm frontend.
    let subset_set: HashSet<ModuleId> = subset_mods.iter().copied().collect();
    let server_ctors: Vec<&str> = server.iter().map(|(n, _)| n.as_str()).collect();
    for m in &pure_sibling_mods {
        let rel = module_relpath(&db.module_name(*m).to_string());
        let mparse = db.module_parse(*m);
        let text = mparse.syntax().text().to_string();
        // The BACKEND copy is verbatim EXCEPT for `Shared`'s ownership of a moved
        // union: strip the union's declaration (the Msg module declares it; only
        // `Shared` keeps a copy) and import `Name(..)` from `Shared` wherever the
        // module references it (the native server reads the same single type).
        let backend_text = own_moved_nominals_verbatim(&mparse.tree(), &text, &moved_unions);
        write(&format!("backend/src/{rel}"), &backend_text, &mut files)?;
        let frontend_text = if Some(*m) == msg_module {
            // The pure Msg module KEEPS its copied STRUCTURAL wire decls (its
            // `exposing (..)` consumers read them from here), so `strips_self =
            // false`; but a moved UNION is stripped by the inject helper and
            // imported `Name(..)` from `Shared` (its single definition).
            let ms_name = db.module_name(*m).to_string();
            let ms_expose = shared_expose_clause(
                &text,
                &ms_name,
                &copied_name_source,
                &generated,
                &moved_unions,
                false,
            );
            inject_applied_variants_into_module(
                &mparse.tree(),
                &text,
                &server,
                &ms_expose,
                &moved_unions,
                &server_internal,
            )
        } else if strip_init_cmd && !init_in_entry && *m == init_mod {
            // GAP-2: the sibling that declares `init` reads through a
            // backend-only `db` CAF. Copied verbatim it would leave
            // `Undefined name: db` + the server-only `Db.query` kernel in the
            // wasm frontend. Strip its command to `Cmd.none` (the server
            // settles the read + embeds `#sky-model`) and drop the now-dangling
            // server-only / backend-only imports — the same treatment an ENTRY
            // `init` already gets in `gen_frontend`. (This module never declares
            // the moved union — the Msg module does — so the moved-union pass
            // below only needs to inject its `Shared` import.)
            let stripped_init = frontend_sibling_with_stripped_init(
                &text,
                &mparse.tree(),
                &no_frontend_names,
            )
            .unwrap_or_else(|| text.clone());
            own_moved_nominals_verbatim(&mparse.tree(), &stripped_init, &moved_unions)
        } else {
            own_moved_nominals_verbatim(&mparse.tree(), &text, &moved_unions)
        };
        write(&format!("frontend/src/{rel}"), &frontend_text, &mut files)?;
    }
    for m in &tainted_mods {
        let rel = module_relpath(&db.module_name(*m).to_string());
        let mparse = db.module_parse(*m);
        let text = mparse.syntax().text().to_string();
        // The backend always carries the FULL tainted module (it runs the effect).
        // A moved union it declares is stripped (owned by `Shared`) and any moved
        // union it references is imported `Name(..)` from `Shared` — the native
        // server sees the same single definition the RPC fold does.
        let backend_text = own_moved_nominals_verbatim(&mparse.tree(), &text, &moved_unions);
        write(&format!("backend/src/{rel}"), &backend_text, &mut files)?;
        // The frontend gets a client subset only when this module was routed for
        // per-binding emission (GAP-1 / GAP-2); otherwise it stays backend-only.
        if subset_set.contains(m) {
            let mname = db.module_name(*m).to_string();
            let empty = HashSet::new();
            let tainted_here = tainted_by_module.get(&mname).unwrap_or(&empty);
            let regen_here = *m == update_module && !update_in_entry;
            let inject_here = Some(*m) == msg_module;
            // A tainted subset STRIPS its own copied decls, so it re-reads them
            // from `Shared` (`strips_self = true`); a copied name it still gets
            // from another module's surviving `exposing (..)` is excluded.
            let sub_expose = shared_expose_clause(
                &text,
                &mname,
                &copied_name_source,
                &generated,
                &moved_unions,
                true,
            );
            let subset = render_module_client_subset(
                &mparse.tree(),
                &text,
                tainted_here,
                &copied_names,
                &server,
                &server_ctors,
                regen_here,
                inject_here,
                &sub_expose,
                &moved_unions,
                &msg_param,
                &model_param,
                &update_anno,
                &no_frontend_names,
                has_rpc_error,
                &server_internal,
                &model_field_names,
                &client_result_map,
            )?;
            write(&format!("frontend/src/{rel}"), &subset, &mut files)?;
        }
    }

    // ---- propagate shipped assets (Bundle.withAsset / withAssetDir) ----
    // The `bundle` binding already copies into frontend/src/Main.sky verbatim, so
    // `sky build --target` (run on frontend/) reads the SAME declarations; copy
    // the declared asset files/dirs alongside it so it can stage them into dist/.
    propagate_bundle_assets(&src, project_dir, &out_dir.join("frontend"))?;

    // ---- FINDING C: propagate the app's DECLARED static-file dir into dist/ ----
    // A `Std.App` web app can serve a static-file directory (WebConfig `static` /
    // `Live.withStatic`, mounted at `staticUrl`, default `/static`; or sky.toml
    // `[live] static`). The Live runtime mounts it; the split has NO Live mount —
    // the generated backend only serves the frontend `dist/`. So the declared
    // static dir must be copied into `dist/<mount-prefix>/`, or every asset the
    // Live build served 404s under `--target web:app`. (Assets the compiler
    // cannot SEE — e.g. a reverse-proxy / Caddy static route configured only at
    // deploy time, as with sky-lang.org's `/brand/…` — are out of reach here by
    // construction: declare them to the app, or keep serving them at the proxy.)
    propagate_static_dir(&src, project_dir, &out_dir.join("frontend"))?;

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
        warnings,
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

/// The app's declared static-file directory + its URL mount prefix, or `None`.
/// Sources, in order: the entry's `App.withConfig (WebConfig { static = Just
/// "<dir>", staticUrl = Just "<url>" })` fields (the Std.App shape), then
/// `sky.toml` `[live] static`. The prefix defaults to `/static` (the Live
/// runtime default, live.go). The returned prefix is normalised — leading and
/// trailing slashes trimmed — so it is the relative path under `dist/` the dir
/// is copied into (`""` = the dist root, when the app mounts static at `/`).
fn app_static_mount(src: &str, project_dir: &Path) -> Option<(String, String)> {
    let dir = scan_config_string_field(src, "static").or_else(|| toml_live_static(project_dir))?;
    let url = scan_config_string_field(src, "staticUrl").unwrap_or_else(|| "/static".to_string());
    Some((dir, url.trim_matches('/').to_string()))
}

/// Find a record field `field = Just "<s>"` / `field = "<s>"` in `src` and return
/// `<s>`. `field` is matched as a whole word (so `static` does not match
/// `staticUrl`), comments stripped first. Returns `None` when the field is
/// absent or set to a non-string (`Nothing`, the `webDefaults` default) — the
/// non-string guard stops it scooping up an unrelated later literal.
fn scan_config_string_field(src: &str, field: &str) -> Option<String> {
    let src = strip_sky_comments(src);
    let src = src.as_str();
    let bytes = src.as_bytes();
    let is_word = |b: u8| b.is_ascii_alphanumeric() || b == b'_';
    let mut from = 0;
    while let Some(rel) = src[from..].find(field) {
        let at = from + rel;
        from = at + field.len();
        let before_ok = at == 0 || !is_word(bytes[at - 1]);
        let after_ok = bytes
            .get(at + field.len())
            .map(|b| !is_word(*b))
            .unwrap_or(true);
        if !before_ok || !after_ok {
            continue;
        }
        // Expect `= [Just] "<s>"`, only whitespace / the `Just` ctor between.
        let mut rest = src[at + field.len()..].trim_start();
        let Some(after_eq) = rest.strip_prefix('=') else {
            continue;
        };
        rest = after_eq.trim_start();
        if let Some(j) = rest.strip_prefix("Just") {
            rest = j.trim_start();
        }
        // The next char MUST open a string literal — otherwise this field is not
        // a string (e.g. `Nothing`) and we do not scan past it.
        if let Some(after_q) = rest.strip_prefix('"') {
            if let Some(end) = after_q.find('"') {
                return Some(after_q[..end].to_string());
            }
        }
    }
    None
}

/// `sky.toml` `[live] static = "<dir>"`, or `None`. A section-scoped scan — the
/// field is only read while inside the `[live]` table.
fn toml_live_static(project_dir: &Path) -> Option<String> {
    let toml = std::fs::read_to_string(project_dir.join("sky.toml")).ok()?;
    let mut in_live = false;
    for line in toml.lines() {
        let l = line.trim();
        if l.starts_with('[') {
            in_live = l == "[live]";
            continue;
        }
        if in_live {
            if let Some(rest) = l.strip_prefix("static") {
                // `static` (not `staticUrl`): the next non-space must be `=`.
                if let Some(v) = rest.trim_start().strip_prefix('=') {
                    let v = v.trim().trim_matches('"');
                    if !v.is_empty() {
                        return Some(v.to_string());
                    }
                }
            }
        }
    }
    None
}

/// Copy the app's declared static-file directory (see [`app_static_mount`], read
/// from `entry_src` + `source_root`) into `dist/<mount-prefix>/`, so the split
/// backend's `Server.static "/" "../frontend/dist"` serves it same-origin at the
/// SAME URL the Live runtime mounted it — closing the `--target web:app` 404 for
/// every asset the Live build served (FINDING C). No-op when the app declares no
/// static dir, or the declared dir is absent (a missing declared dir is an
/// authoring/deploy concern, not a split failure — the Live build would 404 it
/// identically). `dist/` need not exist yet: `copy_tree` creates it, and the
/// frontend build (`stage_web_bundle`) only replaces the wasm + index.html,
/// leaving these files in place; called after that build, they simply coexist.
fn copy_static_dir(entry_src: &str, source_root: &Path, dist: &Path) -> Result<(), String> {
    let Some((dir, rel)) = app_static_mount(entry_src, source_root) else {
        return Ok(());
    };
    let from = source_root.join(&dir);
    if !from.is_dir() {
        return Ok(());
    }
    let to = if rel.is_empty() {
        dist.to_path_buf()
    } else {
        dist.join(&rel)
    };
    copy_tree(&from, &to)
}

/// The split generator's own static-dir propagation: reads the entry it was
/// given (the direct `sky spa-split` path, whose entry still carries the
/// `WebConfig` static declaration, or an app whose static dir is declared in
/// `sky.toml [live]`) and copies into `frontend/dist`. For the `--target
/// web:app` path the entry generate sees is the SYNTHESISED Spa entry, which no
/// longer carries the `App.withConfig` declaration — so that path drives the copy
/// from the ORIGINAL entry via [`stage_declared_static_into_dist`] instead; this
/// call then no-ops (nothing declared in the synthesised source / staged toml).
fn propagate_static_dir(src: &str, project_dir: &Path, frontend_dir: &Path) -> Result<(), String> {
    copy_static_dir(src, project_dir, &frontend_dir.join("dist"))
}

/// Copy the app's declared static dir into `out_dir/frontend/dist` from the
/// ORIGINAL project (`entry_src` + `source_root`) — the authority for a
/// `--target web:app` build, whose synthesised Spa entry has DROPPED the
/// `App.withConfig (WebConfig { static = … })` declaration the split generator
/// would otherwise read. Called by `sky build` after the split + frontend build
/// so the generated backend serves the same static assets the Live build did
/// (FINDING C). No-op when the app declares no static dir.
pub fn stage_declared_static_into_dist(
    entry_src: &str,
    source_root: &Path,
    out_dir: &Path,
) -> Result<(), String> {
    copy_static_dir(entry_src, source_root, &out_dir.join("frontend").join("dist"))
}

/// The app's declared static mount `(dir, url-prefix)`, read from the ORIGINAL
/// entry + its `sky.toml` — the value the `--target web:app` caller passes into
/// [`generate`] as the `static_mount_override`, because the synthesised Spa entry
/// the split sees has dropped the `App.withConfig (WebConfig { static = … })`
/// declaration. `None` when the app declares no static dir.
pub fn declared_static_mount(entry_src: &str, source_root: &Path) -> Option<(String, String)> {
    app_static_mount(entry_src, source_root)
}

/// Copy the app's declared static dir into `out_dir/backend/<dir>` — the LIVE
/// dir the generated backend serves at request time (see the `static_mount` route
/// in [`gen_backend`]) and the SAME cwd-relative dir the app's runtime writes
/// (`File.writeFile "public/…"`) land in, since the backend runs with its own dir
/// as cwd. This seeds the committed assets alongside the runtime uploads so both
/// serve from one place. Read from the ORIGINAL entry (`entry_src` + `source_root`)
/// for the same reason as [`stage_declared_static_into_dist`]: the synthesised Spa
/// entry has dropped the declaration. No-op when the app declares no static dir,
/// the declared dir is absent, or the mount prefix is empty (the app mounts static
/// at `/`, so no live mount is emitted and the dist copy is the only route).
pub fn stage_declared_static_into_backend(
    entry_src: &str,
    source_root: &Path,
    out_dir: &Path,
) -> Result<(), String> {
    let Some((dir, prefix)) = app_static_mount(entry_src, source_root) else {
        return Ok(());
    };
    if prefix.is_empty() {
        return Ok(());
    }
    let from = source_root.join(&dir);
    if !from.is_dir() {
        return Ok(());
    }
    copy_tree(&from, &out_dir.join("backend").join(&dir))
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

/// Read the project's record + union declarations from `mods` into a
/// [`ProjectShapes`] — the raw material the codec resolver uses to AUTO-DERIVE a
/// record codec (§14 #2, option B). Reads the CST only (no lowering), so a record
/// alias's field kinds and a union's constructors are recovered without touching
/// the type solver.
fn build_project_shapes(db: &SkyDatabase, mods: &[ModuleId]) -> ProjectShapes {
    use syntax::ast::Decl;
    // Pass 1: collect the raw field lists (name + head-con + surface) and the
    // union constructor tables, plus the record/union NAME sets.
    let mut raw_records: Vec<(String, Vec<(String, Option<String>, String)>)> = Vec::new();
    let mut record_names: HashSet<String> = HashSet::new();
    let mut union_names: HashSet<String> = HashSet::new();
    let mut unions: HashMap<String, Vec<(String, bool)>> = HashMap::new();
    for &mid in mods {
        let parse = db.module_parse(mid);
        let file = parse.tree();
        let src = parse.syntax().text().to_string();
        for d in file.decls() {
            match d {
                Decl::Alias(a) => {
                    let Some(name) = a.name().map(|n| n.text().to_string()) else {
                        continue;
                    };
                    let Some(fields) = record_alias_fields(&a, &src) else {
                        continue; // not a record alias (e.g. `type alias Id = Int`)
                    };
                    record_names.insert(name.clone());
                    raw_records.push((name, fields));
                }
                Decl::Union(u) => {
                    let Some(name) = u.name().map(|n| n.text().to_string()) else {
                        continue;
                    };
                    let ctors: Vec<(String, bool)> = u
                        .variants()
                        .iter()
                        .filter_map(|v| {
                            let cn = v.name()?.text().to_string();
                            let nullary = v
                                .syntax()
                                .children()
                                .filter_map(syntax::ast::Type::cast)
                                .next()
                                .is_none();
                            Some((cn, nullary))
                        })
                        .collect();
                    union_names.insert(name.clone());
                    unions.insert(name, ctors);
                }
                _ => {}
            }
        }
    }
    // Pass 2: classify each record field now that every record/union name is
    // known, and build the field-SET → unique-name index (dropping any set
    // shared by two records — an ambiguous match must fail closed, not guess).
    let mut records: HashMap<String, Vec<(String, FieldKind)>> = HashMap::new();
    let mut by_fields_multi: HashMap<BTreeSet<String>, Vec<String>> = HashMap::new();
    for (name, raw) in raw_records {
        let set: BTreeSet<String> = raw.iter().map(|(f, _, _)| f.clone()).collect();
        by_fields_multi.entry(set).or_default().push(name.clone());
        let classified: Vec<(String, FieldKind)> = raw
            .into_iter()
            .map(|(f, head, surface)| {
                (f, classify_field_kind(head.as_deref(), &surface, &record_names, &union_names))
            })
            .collect();
        records.insert(name, classified);
    }
    let record_by_fields: HashMap<BTreeSet<String>, String> = by_fields_multi
        .into_iter()
        .filter_map(|(set, names)| {
            if names.len() == 1 {
                Some((set, names.into_iter().next().unwrap()))
            } else {
                None
            }
        })
        .collect();
    ProjectShapes {
        records,
        record_by_fields,
        unions,
    }
}

/// The declared fields of a record-alias `type alias N = { … }` — each as
/// `(field-name, head-constructor, surface-text)`. `None` when the alias body is
/// not a record (a `List`/function/primitive alias).
fn record_alias_fields(
    a: &syntax::ast::AliasDecl,
    src: &str,
) -> Option<Vec<(String, Option<String>, String)>> {
    let mut ty = a.ty()?;
    while let syntax::ast::Type::Paren(p) = &ty {
        ty = p.syntax().children().find_map(syntax::ast::Type::cast)?;
    }
    let rec = match ty {
        syntax::ast::Type::Record(r) => r,
        _ => return None,
    };
    let mut out: Vec<(String, Option<String>, String)> = Vec::new();
    for f in rec
        .syntax()
        .children()
        .filter(|c| c.kind() == SyntaxKind::TypeRecordField)
    {
        let Some(fname) = f
            .children_with_tokens()
            .filter_map(|e| e.into_token())
            .find(|t| t.kind() == SyntaxKind::LowerIdent)
            .map(|t| t.text().to_string())
        else {
            continue;
        };
        let fty = f.children().find_map(syntax::ast::Type::cast);
        let head = fty.as_ref().and_then(type_head_name);
        let surface = fty
            .as_ref()
            .map(|t| slice(src, t.syntax()).trim().to_string())
            .unwrap_or_else(|| "?".to_string());
        out.push((fname, head, surface));
    }
    Some(out)
}

/// The head (outermost) constructor NAME of a CST type, tail-normalised
/// (`List Todo` → `List`, `Maybe (List X)` → `Maybe`, `Types.Foo` → `Foo`).
/// `None` for a function / tuple / var / unit / inline-record head.
fn type_head_name(t: &syntax::ast::Type) -> Option<String> {
    use syntax::ast::Type;
    match t {
        Type::App(a) => a
            .syntax()
            .children()
            .find_map(Type::cast)
            .and_then(|inner| type_head_name(&inner)),
        Type::Paren(p) => p
            .syntax()
            .children()
            .find_map(Type::cast)
            .and_then(|inner| type_head_name(&inner)),
        Type::Con(_) | Type::Qual(_) => t
            .syntax()
            .descendants_with_tokens()
            .filter_map(|e| e.into_token())
            .filter(|tok| tok.kind() == SyntaxKind::UpperIdent)
            .last()
            .map(|tok| tok.text().to_string()),
        _ => None,
    }
}

/// Classify one record field's declared type into its blank default class.
fn classify_field_kind(
    head: Option<&str>,
    surface: &str,
    records: &HashSet<String>,
    unions: &HashSet<String>,
) -> FieldKind {
    match head {
        Some("String") => FieldKind::Str,
        Some("Int") => FieldKind::Int,
        Some("Float") => FieldKind::Float,
        Some("Bool") => FieldKind::Bool,
        Some("List") => FieldKind::ListLike,
        Some("Maybe") => FieldKind::MaybeLike,
        Some(h) if records.contains(h) => FieldKind::Record(h.to_string()),
        Some(h) if unions.contains(h) => FieldKind::Union(h.to_string()),
        _ => FieldKind::Unsupported(surface.to_string()),
    }
}

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

/// The transitive, project-local, PURE value-def closure of the codec bindings
/// the wire references, GROUPED by the module that declares each def. Only defs
/// living in `copy_mods` are copied (a def in a pure sibling is reached via an
/// `import`, not a copy, and is skipped here — the historical entry-only
/// behaviour, now generalised to the entry PLUS the mixed modules that own a
/// referenced codec).
///
/// Fails closed: if a referenced codec's transitive closure reaches a
/// SERVER-TAINTED def (in a `copy_mods` module), that codec cannot be copied
/// without dragging an effect into `Shared` — so the whole split is refused with
/// the same actionable "no codec" wording, rather than emit a `Shared` that
/// would not compile or that would leak. `tainted_by_module` maps a module name
/// to its server-tainted top-level binding names.
fn compute_value_copy(
    db: &SkyDatabase,
    copy_mods: &BTreeSet<ModuleId>,
    registry: &[CodecBinding],
    needed: &BTreeSet<String>,
    tainted_by_module: &HashMap<String, HashSet<String>>,
) -> Result<BTreeMap<ModuleId, BTreeSet<String>>, String> {
    let mut result: BTreeMap<ModuleId, BTreeSet<String>> = BTreeMap::new();
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
        // A def in a module we do not copy from (a pure sibling, or an unrelated
        // module) is reached via an import, not a copy — skip it.
        if !copy_mods.contains(&loc.module) {
            continue;
        }
        let name = loc.name.as_str().to_string();
        let mname = db.module_name(loc.module).to_string();
        // A copied codec's closure that reaches a server-tainted def is NOT
        // eligible — copying it would drag the effect into `Shared`. Fail closed.
        if tainted_by_module
            .get(&mname)
            .map(|s| s.contains(&name))
            .unwrap_or(false)
        {
            return Err(format!(
                "no codec: a referenced `Codec` binding reaches the server-tainted def `{name}` in module `{mname}`, so it cannot be copied into `Shared` without leaking a server effect into the wasm frontend. Move the codec (and its pure helpers) into a module that runs no effect, or reduce the wire field to a primitive / `List` / `Maybe`."
            ));
        }
        result.entry(loc.module).or_default().insert(name);
        for c in spa_partition::body_def_callees(db, loc.module, d) {
            if !seen.contains(&c) {
                work.push(c);
            }
        }
    }
    Ok(result)
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
    project_siblings: &HashSet<String>,
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
        // Drop EVERY project sibling module import that the wire does not need
        // (a needed one is re-added below with a canonical `exposing (..)`).
        // `Shared` is pure wire records + codecs and only references stdlib +
        // the `needed_siblings`; carrying the entry's other sibling imports
        // (`import Data`, `import Routes`, `import State`) is both unnecessary
        // and unsound — those modules transitively import the `Msg` module,
        // whose frontend copy now imports `Shared` for the injected
        // `Applied<Msg>` variants (GAP-A), so a carried sibling import forms a
        // `Shared` → sibling → `Msg` → `Shared` cycle (E1010). By the design's
        // own invariant (needed_siblings = the siblings the wire references) a
        // copied codec/type body never references a non-needed sibling, so
        // dropping them is sound.
        if project_siblings.contains(&i.module_path) {
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

/// The GENERATED wire names `Shared` publishes for each server branch: the
/// per-branch `<Msg>Req` / `<Msg>Resp` records and their codecs. Every module
/// that consumes the wire (the entry's regenerated `update`, a sibling `update`,
/// the injected `Applied<Msg>` variants) references a subset of these, so they
/// are ALWAYS in a consumer's `import Shared exposing (…)` list — unlike the
/// COPIED user types, which a consumer may already read from their origin module.
fn generated_wire_names(server: &[(String, BranchIo)]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for (name, _) in server {
        out.push(format!("{name}Req"));
        out.push(format!("{}ReqCodec", lower_first(name)));
        out.push(format!("{name}Resp"));
        out.push(format!("{}RespCodec", lower_first(name)));
    }
    out
}

/// True when `src` imports module `module_name` with an `exposing (..)` clause —
/// i.e. every name that module exports is in unqualified scope here. Used to
/// decide whether a copied name is still reachable from its origin in a consumer
/// (so it must NOT also be imported from `Shared`, which would be an ambiguous
/// double-import). An explicit `exposing (Foo)` import does NOT count: the split
/// strips copied names out of explicit lists, so the name is no longer reachable
/// through it.
fn imports_module_exposing_all(src: &str, module_name: &str) -> bool {
    for line in src.lines() {
        let t = line.trim_start();
        let Some(rest) = t.strip_prefix("import ") else {
            continue;
        };
        if rest.split_whitespace().next() != Some(module_name) {
            continue;
        }
        if let Some(open) = rest.find("exposing") {
            let after = &rest[open + "exposing".len()..];
            if let Some(lp) = after.find('(') {
                let inner = &after[lp + 1..];
                if let Some(rp) = inner.find(')') {
                    if inner[..rp].trim() == ".." {
                        return true;
                    }
                }
            }
        }
    }
    false
}

/// The `import Shared exposing (…)` clause a consumer module needs.
///
/// `Shared` publishes BOTH the generated wire names (always needed by a consumer
/// that touches the wire) AND copies of user types the wire references. A copied
/// name must be imported from `Shared` ONLY when the consumer has otherwise LOST
/// it, never when it still reads it from the type's origin module — importing the
/// same structural type from two places is an ambiguous double-import.
///
///   * A name whose origin is THIS module: needed iff `strips_self` (the entry /
///     a tainted subset strips its own copied decls, so it re-reads them from
///     `Shared`; the pure Msg module KEEPS its decls, so it does not).
///   * A name from another origin: needed iff this module does NOT still import
///     that origin via `exposing (..)` (an explicit import of the name was
///     stripped, so it is gone; a surviving `exposing (..)` still provides it).
///
/// A MOVED UNION (`moved_unions`) is handled separately from the structural
/// rule: `Shared` owns its single definition, so its origin no longer declares
/// it and `exposing (..)` cannot surface it. Any module that references the type
/// name OR one of its constructors therefore imports `Name(..)` from `Shared`
/// (the `(..)` brings the constructors so a `case`/construct still resolves).
///
/// Falls back to `exposing (..)` only when the resulting list is empty (a
/// server-less app whose `Shared` exports nothing), which is a valid header.
fn shared_expose_clause(
    module_src: &str,
    self_name: &str,
    copied_name_source: &BTreeMap<String, String>,
    generated: &[String],
    moved_unions: &BTreeMap<String, Vec<String>>,
    strips_self: bool,
) -> String {
    let mut names: Vec<String> = generated.to_vec();
    for (n, origin) in copied_name_source {
        // A moved union is imported via the `(..)` form below, never as a plain
        // structural name — skip it here so it is not added twice / bare.
        if moved_unions.contains_key(n) {
            continue;
        }
        let needed = if origin == self_name {
            strips_self
        } else {
            !imports_module_exposing_all(module_src, origin)
        };
        if needed {
            names.push(n.clone());
        }
    }
    for (u, words) in moved_unions {
        if words.iter().any(|w| module_mentions_word(module_src, w)) {
            names.push(format!("{u}(..)"));
        }
    }
    let mut seen: HashSet<String> = HashSet::new();
    names.retain(|n| seen.insert(n.clone()));
    if names.is_empty() {
        "import Shared exposing (..)".to_string()
    } else {
        format!("import Shared exposing ({})", names.join(", "))
    }
}

/// True when module source `src` mentions the identifier `word` at a token
/// boundary, ignoring `--` line comments. Used to decide whether a module copy
/// references a moved union (by its type name or a constructor) and so must
/// import it from `Shared`. A qualified reference (`Foo.word`) is NOT a match —
/// a moved union is imported UNQUALIFIED, so a dotted occurrence is a different
/// name (and a lowercase field like `.page` never collides with a `Page` type).
fn module_mentions_word(src: &str, word: &str) -> bool {
    if word.is_empty() {
        return false;
    }
    for line in src.lines() {
        // Drop a `--` line comment (a crude but safe over-approximation: a `--`
        // inside a string literal only causes us to MISS a mention, which at
        // worst omits an import the compiler would then flag — never a false
        // resolve). Keeps a comment-only mention from forcing a spurious import.
        let code = match line.find("--") {
            Some(i) => &line[..i],
            None => line,
        };
        let bytes = code.as_bytes();
        let mut start = 0usize;
        while let Some(rel) = code[start..].find(word) {
            let at = start + rel;
            let before = code[..at].chars().last();
            let after_idx = at + word.len();
            let after = code[after_idx..].chars().next();
            let ok_before = before
                .map(|c| !c.is_alphanumeric() && c != '_' && c != '.')
                .unwrap_or(true);
            let ok_after = after.map(|c| !c.is_alphanumeric() && c != '_').unwrap_or(true);
            if ok_before && ok_after {
                return true;
            }
            // Advance past this occurrence (bytes are ASCII for identifiers).
            start = at + word.len().max(1);
            if start >= bytes.len() {
                break;
            }
        }
    }
    false
}

/// Reassemble a module's source with the declarations named in `names` REMOVED
/// (both a `type`/`union`/`alias` decl and any same-named value/annotation).
/// Used to give up OWNERSHIP of a moved union: `Shared` declares it once, so
/// every other copy must not re-declare it. `file` MUST be the parse of `src`
/// (the caller applies this to a VERBATIM copy, where tree and text agree).
fn strip_decls_by_name(file: &SourceFile, src: &str, names: &BTreeSet<String>) -> String {
    if names.is_empty() {
        return src.to_string();
    }
    let first_decl_start = file
        .decls()
        .next()
        .map(|d| usize::from(d.syntax().text_range().start()))
        .unwrap_or(src.len());
    let prefix = src[..first_decl_start].trim_end();
    let mut out = String::with_capacity(src.len());
    out.push_str(prefix);
    out.push_str("\n\n\n");
    for d in file.decls() {
        if let Some(n) = decl_name(&d) {
            if names.contains(&n) {
                continue;
            }
        }
        out.push_str(slice(src, d.syntax()).trim_end());
        out.push_str("\n\n\n");
    }
    out
}

/// Apply `Shared`'s ownership of the moved unions to a VERBATIM module copy (a
/// pure sibling, or a tainted module's full backend copy). `file` MUST be the
/// parse of `text`. Two steps:
///   1. strip any moved-union declaration this module makes (`Shared` owns it),
///   2. when the copy references a moved union (type or constructor), ensure
///      `import Shared exposing (Name(..), …)` so the reference resolves to
///      `Shared`'s single definition.
/// A module that neither declares nor references a moved union is returned
/// unchanged — no `Shared` import is added (which would be needless, and could
/// re-open a cycle for a module `Shared` itself imports).
fn own_moved_nominals_verbatim(
    file: &SourceFile,
    text: &str,
    moved_unions: &BTreeMap<String, Vec<String>>,
) -> String {
    if moved_unions.is_empty() {
        return text.to_string();
    }
    let declared: BTreeSet<String> = file
        .decls()
        .filter_map(|d| decl_name(&d))
        .filter(|n| moved_unions.contains_key(n))
        .collect();
    let stripped = strip_decls_by_name(file, text, &declared);
    let mut exposes: Vec<String> = Vec::new();
    for (u, words) in moved_unions {
        if words.iter().any(|w| module_mentions_word(&stripped, w)) {
            exposes.push(format!("{u}(..)"));
        }
    }
    if exposes.is_empty() {
        return stripped;
    }
    let clause = format!("import Shared exposing ({})", exposes.join(", "));
    ensure_import_present(&stripped, "Shared", &clause)
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

/// GAP-2: transform a SIBLING module's FRONTEND copy when that module declares
/// the app's `init` and the init command must be stripped (a GET-safe read
/// through a backend-only `db` CAF). Rewrites the `init` value decl's command to
/// `Cmd.none` in place, then drops the imports that the strip leaves dangling —
/// a backend-only PROJECT module (never present in the frontend tree) or a
/// server-only module no longer referenced. Returns `None` if the module has no
/// `init` value decl in the expected `( model, cmd )` shape (the caller then keeps
/// the module verbatim so a genuine mismatch surfaces as a normal compile error).
fn frontend_sibling_with_stripped_init(
    msrc: &str,
    mfile: &SourceFile,
    backend_only: &HashSet<String>,
) -> Option<String> {
    let init_decl = mfile
        .decls()
        .find(|d| decl_name(d).as_deref() == Some("init") && is_value_decl(d))?;
    let rewritten = frontend_init_value_without_cmd(msrc, &init_decl)?;
    let node = init_decl.syntax();
    let a = u32::from(node.text_range().start()) as usize;
    let b = u32::from(node.text_range().end()) as usize;
    let mut out = String::with_capacity(msrc.len());
    out.push_str(&msrc[..a]);
    out.push_str(&rewritten);
    out.push_str(&msrc[b..]);
    Some(drop_dangling_sibling_imports(&out, backend_only))
}

/// Drop `import` lines that a sibling's init-strip leaves dangling: a backend-only
/// PROJECT module (which is never emitted into the wasm frontend, so any import of
/// it is `E1001`) always goes; a server-only module (`Std.Db`, `Sky.Core.File`, …)
/// goes only when none of the names it binds (its alias + any `exposing (…)`
/// names) is still referenced in the module's code after the strip. Mirrors the
/// server-only import drop `gen_frontend` applies to the entry, but usage-gated so
/// a legitimately-used server-only import (a pure sibling reading a literal path)
/// is untouched.
fn drop_dangling_sibling_imports(src: &str, backend_only: &HashSet<String>) -> String {
    // The module's code with comments + import lines removed, so an import's own
    // text does not count as a reference to itself.
    let body: String = strip_sky_comments(src)
        .lines()
        .filter(|l| !l.trim_start().starts_with("import "))
        .collect::<Vec<_>>()
        .join("\n");
    let mut out = String::new();
    for line in src.lines() {
        if let Some(rest) = line.trim_start().strip_prefix("import ") {
            let path = rest.split_whitespace().next().unwrap_or("");
            if backend_only.contains(path) {
                continue; // never exists in the frontend tree
            }
            if is_server_only_module(path) {
                let names = import_referenceable_names(rest);
                if !names.iter().any(|n| references_word(&body, n)) {
                    continue; // unused after the strip
                }
            }
        }
        out.push_str(line);
        out.push('\n');
    }
    out
}

/// The names an `import` clause binds into scope: its alias (`import X.Y as A` → `A`,
/// else the last path segment `Y`) plus every name in an `exposing (…)` list
/// (`Foo(..)` → `Foo`). `rest` is the import text after the `import ` keyword.
fn import_referenceable_names(rest: &str) -> Vec<String> {
    let mut names: Vec<String> = Vec::new();
    let path = rest.split_whitespace().next().unwrap_or("");
    // alias, else the last dotted segment.
    let alias = rest
        .split_whitespace()
        .position(|t| t == "as")
        .and_then(|i| rest.split_whitespace().nth(i + 1))
        .map(|s| s.to_string())
        .unwrap_or_else(|| path.rsplit('.').next().unwrap_or(path).to_string());
    if !alias.is_empty() {
        names.push(alias);
    }
    if let Some(open) = rest.find("exposing") {
        if let Some(lp) = rest[open..].find('(') {
            let after = &rest[open + lp + 1..];
            if let Some(rp) = after.rfind(')') {
                for tok in after[..rp].split(',') {
                    let name: String = tok
                        .trim()
                        .chars()
                        .take_while(|c| c.is_ascii_alphanumeric() || *c == '_')
                        .collect();
                    if !name.is_empty() {
                        names.push(name);
                    }
                }
            }
        }
    }
    names
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
    // Strip comments FIRST. This scan now runs over whole module sources
    // (spa_split::generate scans every project module so a sibling `Routes.sky`
    // is seen), and a `--`/`{- -}` comment that mentions `App.route "…"` — a
    // docstring, a code sample — would otherwise be scraped as a real pattern
    // and emitted as a malformed `Server.api "GET …"` the Go mux rejects.
    let routes_src = strip_sky_comments(routes_src);
    let routes_src = routes_src.as_str();
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
                    // A valid route pattern is an absolute path (`/…`); requiring
                    // the leading slash rejects a stray non-path literal the scan
                    // might otherwise pick up and hand to the Go mux.
                    if pat.starts_with('/') && pat != "/" && !out.contains(&pat.to_string()) {
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

/// The PATHS of every `App.api "GET <path>" …` / `Spa.api "GET <path>" …`
/// endpoint in `routes_src`, its method verb parsed off the `"METHOD /path"`
/// literal — the set the per-route SSR page mounts must NOT duplicate (FINDING
/// A). Only GET endpoints are returned: the SSR page mounts are all
/// `GET <path>`, so a GET api endpoint on the same path is the sole collision
/// that double-registers a mux pattern; a `POST`/`PUT`/… endpoint on a page path
/// is a distinct pattern. Comments are stripped first (a `--` / `{- -}` sample
/// mentioning `App.api` must not be scraped as a real endpoint), matching
/// `spa_ssr_route_patterns`. The head match is boundary-guarded so `App.api`
/// does not fire inside the GENERATED `App.apiServerRoute` (this scan runs over
/// original module sources, which have none, but the guard keeps it robust).
fn spa_api_get_paths(routes_src: &str) -> Vec<String> {
    let routes_src = strip_sky_comments(routes_src);
    let routes_src = routes_src.as_str();
    let mut out: Vec<String> = Vec::new();
    for head in ["App.api", "Spa.api"] {
        let mut rest = routes_src;
        while let Some(i) = rest.find(head) {
            let after = &rest[i + head.len()..];
            // Boundary: `App.api` must be followed by the call's argument, i.e.
            // whitespace or `(` (rejects `App.apiServerRoute`).
            let boundary_ok = after
                .chars()
                .next()
                .map(|c| c.is_whitespace() || c == '(')
                .unwrap_or(false);
            if !boundary_ok {
                rest = after;
                continue;
            }
            if let Some(q) = after.find('"') {
                let tail = &after[q + 1..];
                if let Some(end) = tail.find('"') {
                    let lit = &tail[..end];
                    let mut it = lit.split_whitespace();
                    let method = it.next().unwrap_or("");
                    let path = it.next().unwrap_or("");
                    if method.eq_ignore_ascii_case("GET")
                        && path.starts_with('/')
                        && !out.contains(&path.to_string())
                    {
                        out.push(path.to_string());
                    }
                    rest = &tail[end + 1..];
                    continue;
                }
            }
            rest = after;
        }
    }
    out
}

/// Blank out Sky comments (`--` line, `{- -}` block, nestable) so a
/// text-level scan does not read code samples or docstrings as source. String
/// literals are respected (a `--` inside `"…"` is not a comment). A best-effort
/// scanner — triple-quoted strings are not special-cased — sufficient for the
/// route-literal extraction it guards.
fn strip_sky_comments(src: &str) -> String {
    let chars: Vec<char> = src.chars().collect();
    let n = chars.len();
    let mut out = String::with_capacity(src.len());
    let mut i = 0usize;
    let mut in_str = false;
    let mut block_depth = 0usize;
    while i < n {
        let c = chars[i];
        let c2 = if i + 1 < n { Some(chars[i + 1]) } else { None };
        if block_depth > 0 {
            if c == '{' && c2 == Some('-') {
                block_depth += 1;
                i += 2;
                continue;
            }
            if c == '-' && c2 == Some('}') {
                block_depth -= 1;
                i += 2;
                continue;
            }
            // Preserve newlines so line structure is roughly kept.
            if c == '\n' {
                out.push('\n');
            }
            i += 1;
            continue;
        }
        if in_str {
            out.push(c);
            if c == '\\' {
                if let Some(nc) = c2 {
                    out.push(nc);
                    i += 2;
                    continue;
                }
            }
            if c == '"' {
                in_str = false;
            }
            i += 1;
            continue;
        }
        if c == '"' {
            in_str = true;
            out.push(c);
            i += 1;
            continue;
        }
        if c == '-' && c2 == Some('-') {
            while i < n && chars[i] != '\n' {
                i += 1;
            }
            continue;
        }
        if c == '{' && c2 == Some('-') {
            block_depth = 1;
            i += 2;
            continue;
        }
        out.push(c);
        i += 1;
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
    // The `import Shared exposing (…)` clause for the entry (an explicit list that
    // excludes copied names still reachable via a surviving `exposing (..)`).
    shared_expose: &str,
    push_mode: bool,
    broker_url: Option<&str>,
    ssr_route_patterns: &[String],
    init_src: &str,
    // Server branch ctor names whose RPC handler settles a `Cmd.perform` chain
    // server-side (server-internal effect chaining).
    chaining: &HashSet<String>,
    // PATTERN-2 (client-result perform): a server root whose RPC RUNS its server
    // task and answers with the task RESULT (`spaRunPerform_ cmd`), for the
    // frontend to dispatch client-side.
    client_result: &HashMap<String, ClientResultInfo>,
    // The app's declared static-file mount `(dir, url-prefix)` (see
    // [`app_static_mount`]). `Some` → the backend gets a LIVE `Server.static`
    // mount reading that dir from disk at request time, so files WRITTEN at
    // runtime (an admin image upload to `public/products/<uuid>`) serve — exactly
    // as Sky.Live serves its static dir live. The build-time `dist` copy only
    // covers assets present at build. `None` (or an empty prefix) → no live
    // mount, the `dist` catch-all is the only static route.
    static_mount: Option<&(String, String)>,
    // STATELESS SIGNED SESSION: the identity fields the backend signs into the
    // `sky_sid` cookie and verifies on every RPC + SSR (empty → nothing emitted).
    session_proj: &[SessionProjField],
    warnings: &mut Vec<String>,
) -> Result<String, String> {
    // Imports: keep every input import EXCEPT Std.Spa (framework, main-only),
    // then add the server-side machinery.
    let mut import_lines: Vec<String> = imports
        .iter()
        .filter(|i| i.module_path.rsplit('.').next() != Some("Spa"))
        // A type/codec copied into `Shared` arrives via `import Shared exposing
        // (..)`; drop it from any original import (e.g. a verbatim `import Data
        // exposing (Item, itemCodec, …)`) so the backend does not double-import it.
        .map(|i| strip_names_from_import_exposing(&i.text, copied_names))
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
    // Server-internal effect chaining: a chaining branch's handler calls the
    // `Spa_settleServerChain` kernel alias, which needs `Sky.Ffi`.
    let any_chaining = !push_mode && server.iter().any(|(n, _)| chaining.contains(n));
    // PATTERN-2: a client-result root's handler runs `spaRunPerform_`, a
    // `Sky.Ffi` kernel alias, so it needs `Sky.Ffi` too.
    let any_client_result = !push_mode && server.iter().any(|(n, _)| client_result.contains_key(n));
    if any_chaining || any_client_result {
        add(imports, &mut import_lines, "Sky.Ffi", "import Sky.Ffi as Ffi");
    }
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
    // GAP-1: the App→Spa synthesis emits a `spaApiRoutes_` binding (main.rs
    // synthesize_spa_source) when `withRoutes` MIXES page routes with `App.api`
    // server endpoints. The backend mounts those as real `Server` handlers
    // (`List.concatMap App.apiServerRoute spaApiRoutes_`), appended to the
    // `Server.listen` route list — the client (Spa) cannot carry a server handler.
    let has_synth_api_routes = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaApiRoutes_"));
    // Boot setup: the App→Spa synthesis (main.rs synthesize_spa_source) captures
    // the app `main`'s boot-setup `let`-prefix (schema creation, migrations,
    // seeding, env load) into a NAMED, server-tainted `spaBootSetup_ : Task Error
    // ()` binding. The backend `main` must FORCE it before `Server.listen`, so
    // the server runs the setup before it starts serving; otherwise a deployed
    // backend serves empty data (no schema / no seed). It is dropped from the
    // wasm frontend by the taint analysis, so only the backend runs it.
    let has_synth_boot_setup = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaBootSetup_"));
    // Fix 5: the App→Spa synthesis (main.rs synthesize_spa_source) carries
    // `App.withGuard` into a NAMED top-level `spaGuard_` binding so this backend
    // can reference it. It is the per-Msg authorisation guard the backend
    // enforces on every `/_rpc/<Msg>` handler BEFORE `update` runs — the TRUSTED
    // check, because the wasm client is untrusted and can forge any RPC call. A
    // hand-authored Sky.Spa backend has no such binding.
    let has_synth_guard = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaGuard_"));
    // Fix 2: `spaOnRequest_` seeds `init`'s SSR model from the real request
    // (path / query / cookies / headers), and `spaOnNavigate_` is fired per
    // resolved route so per-route data (not just init's path-independent read)
    // is settled into the embedded `#sky-model`. Both settle under the
    // goroutine-local SSR-safe guard (spa_ssr_safe.go), so a destructive effect
    // in the folded command self-suppresses — a GET never mutates.
    let has_synth_on_request = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaOnRequest_"));
    let has_synth_on_navigate = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaOnNavigate_"));
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
    // The `init` source (its declaring module resolved by the caller — `init`
    // may live in a sibling module, the sky-lang.org shape) joins EVERY decl
    // named `init` (annotation + value binding are separate decls) so the scan
    // sees the body — matching only the annotation `init : () -> ( Model, Cmd
    // Msg )` would miss the `File.readFile` in the body.
    let init_get_safe = init_cmd_is_get_safe(init_src);
    // Emit the SSR route for a synthesised (`Std.App`) app when the backend has
    // real per-request work to do: EITHER a server branch / push, OR a GET-safe
    // `init` read to settle (the sky-lang.org content-site shape — a DB read at
    // `init` with a purely-navigational `update` and therefore NO server branch).
    // Without the `init_get_safe` arm that shape falls to a static-only backend
    // (no SSR, no `#sky-model`), so the client-leg strip would compile but the
    // client would boot to empty data. `has_synth_view && has_synth_head` scopes
    // this to the synthesis output, whose `init`/`view` are plain TEA safe to run
    // server-side (a hand-authored Sky.Spa client, whose `init`/`view` reference
    // client-only `Std.Spa`, has no `spaView_`/`spaHead_` and keeps today's shell).
    // Fix 2: a routed `onNavigate` is ALSO real per-request work to settle — the
    // per-route data lives in `onNavigate page`'s command, not in `init`. Its
    // reads settle under the SSR-safe guard, so this needs no static allowlist
    // proof (a destructive effect in the fired command self-suppresses at
    // runtime — spa_ssr_safe.go). So SSR now emits for the onNavigate content-site
    // shape (`init = Cmd.none`, per-route data loaded on navigation) too.
    let nav_ssr = has_synth_routes && has_synth_on_navigate;
    let emit_ssr = (!(server.is_empty() && !push_mode) || init_get_safe || nav_ssr)
        && has_synth_view
        && has_synth_head;
    if emit_ssr {
        add(imports, &mut import_lines, "Sky.Ffi", "import Sky.Ffi as Ffi");
        add(imports, &mut import_lines, "Sky.Core.Task", "import Sky.Core.Task as Task");
        // Fix 7: the SSR first paint embeds the WHOLE model via
        // `Codec.toJson (Codec.auto resolved)` and the client decodes it with the
        // symmetric `Codec.fromJson (Codec.auto blank)`. `Codec.auto` compiles for
        // ANY model, so a field whose type it cannot round-trip (today: the opaque
        // `Secret`) used to fail ONLY at runtime — a console.error + a silent
        // fall-back to `init` (empty) while Sky.Live rendered the real value.
        // Catch it HERE, at `sky build --target web:app`, naming the field + type.
        // (A Secret carried in an RPC read/write set is already a HARD build error
        // in build_wire; this covers the SSR-embed path, which build_wire never
        // sees because it runs `Codec.auto` over the whole value at runtime.)
        for f in model_fields {
            if let Some((ty_label, why)) = codec_auto_unencodable(f) {
                warnings.push(format!(
                    "model field `{}` has type `{}`, which `Codec.auto` cannot round-trip through the Sky.Spa SSR model embed. {}",
                    f.name, ty_label, why
                ));
            }
        }
    }
    if push_mode {
        // Server→client PUSH machinery (docs/skyspa/auto-split.md §16).
        add(imports, &mut import_lines, "Sky.Core.Task", "import Sky.Core.Task as Task");
        add(imports, &mut import_lines, "Sky.Ffi", "import Sky.Ffi as Ffi");
        add(imports, &mut import_lines, "Sky.Core.Maybe", "import Sky.Core.Maybe as Maybe");
        add(imports, &mut import_lines, "Sky.Http.Server.Stream", "import Sky.Http.Server.Stream as Stream exposing (StreamWriter)");
    }
    // STATELESS SIGNED SESSION: the sign / verify path needs Std.Auth (the token
    // logic Sky.Live reuses), the `Secret` type, `Sky.Ffi` (the secret-kernel
    // façade), and `Sky.Core.Task` (the sign-out handler answers a Task). Both
    // Auth and Secret are `Ffi.kernel` façades — they pull no Live runtime into
    // the backend. The `add` helper only checks the ORIGINAL app imports, so a
    // module another block already pushed (Ffi / Task under SSR or push) is
    // filtered against `import_lines` here to avoid a duplicate import line.
    if !session_proj.is_empty() {
        for (path, text) in [
            ("Std.Auth", "import Std.Auth as Auth"),
            ("Sky.Core.Secret", "import Sky.Core.Secret exposing (Secret)"),
            ("Sky.Ffi", "import Sky.Ffi as Ffi"),
            ("Sky.Core.Task", "import Sky.Core.Task as Task"),
        ] {
            if !has_module(imports, path)
                && !import_lines
                    .iter()
                    .any(|l| l.split_whitespace().nth(1) == Some(path))
            {
                import_lines.push(text.to_string());
            }
        }
    }
    import_lines.push(shared_expose.to_string());

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
    //
    // EXCEPTION to the exception: when SSR is emitted for a GET-safe `init` on a
    // synthesised app with no server branch (`emit_ssr && server.is_empty()`), the
    // backend DOES run app logic — the SSR handler settles `init` + renders
    // `spaView_`/`spaHead_` — so it must copy those decls. `emit_ssr`'s
    // `has_synth_view && has_synth_head` guard already restricts this to the
    // synthesis output, whose decls carry no client-only `Std.Spa` reference.
    // An app with `App.api` server endpoints (`spaApiRoutes_`) is NOT static-only:
    // the backend must carry the api route binding + its handlers to mount them,
    // so its (synthesised, TEA-safe) decls are copied like any server-bearing app.
    let static_only_backend = server.is_empty() && !push_mode && !emit_ssr && !has_synth_api_routes;
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
    // Fix 5: the 403 the server-side guard returns when it DENIES a message. A
    // denied `/_rpc/<Msg>` never runs `update` (no effect fires); a denied SSR
    // navigation renders the NotFound shell with no protected data settled.
    if has_synth_guard {
        handlers.push_str("forbidden : String -> Response\nforbidden msg =\n    Server.withStatus 403 (Server.text msg)\n\n\n");
    }

    // Server-internal effect chaining (docs/skyspa/auto-split.md). A server
    // branch that returns `Cmd.perform serverTask ToMsg` (where `ToMsg` is
    // dispatched ONLY server-side) must run the WHOLE chain inside its RPC and
    // answer with the final settled model — mirroring Sky.Live, where the loop
    // is server-side. `spaChainSettle_ model cmd update` folds every
    // server-runnable perform leaf back through `update` to a fixpoint and
    // returns the settled model (runtime-go/rt/spa_chain_notjs.go). `model` is a
    // plain type variable (the concrete Model binds it at the call site).
    if any_chaining {
        handlers.push_str(
            "-- Server-internal effect chaining: settle a `Cmd.perform` chain\n\
             -- server-side to its fixpoint and answer with the final model diff\n\
             -- (runtime-go/rt/spa_chain_notjs.go).\n\
             spaChainSettle_ : model -> any -> any -> ( model, any )\n\
             spaChainSettle_ =\n\
             \x20   Ffi.kernel \"Spa_settleServerChain\"\n\n\n",
        );
    }
    // PATTERN-2 (client-result perform): run the single server task inside a
    // branch's returned command and RETURN its RESULT, for the client to dispatch
    // through `update` (runtime-go/rt/spa_perform_notjs.go). The result type
    // `result` is a plain type variable — the concrete `Result Error T` binds it
    // at the call site (`result = spaRunPerform_ cmd`).
    if any_client_result {
        handlers.push_str(
            "-- Client-result perform: run the server task in the branch's command\n\
             -- and return its RESULT for the client to dispatch (pattern-2,\n\
             -- runtime-go/rt/spa_perform_notjs.go).\n\
             spaRunPerform_ : any -> result\n\
             spaRunPerform_ =\n\
             \x20   Ffi.kernel \"Spa_runServerPerform\"\n\n\n",
        );
    }

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
    // STATELESS SIGNED SESSION machinery — emitted once, only when the projection
    // is non-empty (an app with no server-trusted session is unchanged).
    if !session_proj.is_empty() {
        // The signing secret: the unary `Spa_sessionSecret` kernel
        // (SKY_SPA_SESSION_SECRET >= 32 bytes, else auto-mint + persist under the
        // data dir — runtime-go/rt/spa_session_secret.go) plus a memoised CAF
        // applying it, matching the existing `spaWasmName` shape.
        handlers.push_str(
            "-- STATELESS SIGNED SESSION (security). The backend signs the identity\n\
             -- projection into an httpOnly `sky_sid` cookie on the establishing branch\n\
             -- and VERIFIES it on every RPC + SSR, taking the session from the cookie —\n\
             -- never from the forgeable wire model. No server session store, so the\n\
             -- backend stays stateless; it reuses Sky.Live's own Std.Auth token logic.\n\
             spaSessionSecret_ : () -> Secret\n\
             spaSessionSecret_ =\n\
             \x20   Ffi.kernel \"Spa_sessionSecret\"\n\n\n\
             sessionSecret_ : Secret\n\
             sessionSecret_ =\n\
             \x20   spaSessionSecret_ ()\n\n\n",
        );
        // One verify helper per identity field: return the TRUSTED value from the
        // signed cookie, or the init value when there is no valid cookie — NEVER
        // the wire value. `claims.p<N>` reads the JSON claim keyed by the field's
        // INDEX off the (erased) verifyToken result; `Codec.fromJson <field codec>`
        // round-trips it back to the field type. The claim key is index-based
        // (`p0`, `p1`, …) rather than the field name so the polymorphic claims
        // record cannot unify with the app's nominal `Model` (which carries the
        // identity field under its own name), which would coerce the signed JSON
        // string back into the field's own type.
        for (idx, p) in session_proj.iter().enumerate() {
            let vname = session_verify_name(&p.name);
            let cname = session_codec_name(&p.name);
            handlers.push_str(&format!(
                "{vname} req initVal =\n\
                 \x20   case Server.getCookie \"sky_sid\" req of\n\
                 \x20       Just tok ->\n\
                 \x20           case Auth.verifyToken sessionSecret_ tok of\n\
                 \x20               Ok claims ->\n\
                 \x20                   case Codec.fromJson {cname} claims.p{idx} of\n\
                 \x20                       Ok v ->\n\
                 \x20                           v\n\n\
                 \x20                       Err _ ->\n\
                 \x20                           initVal\n\n\
                 \x20               Err _ ->\n\
                 \x20                   initVal\n\n\
                 \x20       Nothing ->\n\
                 \x20           initVal\n\n\n",
            ));
        }
        // signedResponse_ m resp: re-issue the `sky_sid` cookie from the model the
        // establishing branch produced, signing every identity field through its
        // codec (fixed 30-day expiry; the token `exp` bounds replay). A sign
        // failure (server misconfig) leaves the response cookie-less rather than
        // failing the request.
        let claims = session_proj
            .iter()
            .enumerate()
            .map(|(i, p)| {
                let sep = if i == 0 { "" } else { ", " };
                format!(
                    "{sep}p{i} = Codec.toJson {0} m.{1}",
                    session_codec_name(&p.name),
                    p.name
                )
            })
            .collect::<String>();
        handlers.push_str(&format!(
            "signedResponse_ m resp =\n\
             \x20   case Auth.signToken sessionSecret_ {{ {claims} }} 2592000 of\n\
             \x20       Ok tok ->\n\
             \x20           Server.withCookie \"sky_sid\" tok \"Path=/; HttpOnly; SameSite=Lax\" resp\n\n\
             \x20       Err _ ->\n\
             \x20           resp\n\n\n"
        ));
        // The framework sign-out endpoint: clear `sky_sid` (Max-Age=0). The wasm
        // client calls it when the session field transitions Just -> Nothing
        // (wired in a separate task); emitted only when the projection is present.
        handlers.push_str(
            "spaSignOutHandler : Handler\n\
             spaSignOutHandler _ =\n\
             \x20   Task.succeed\n\
             \x20       (Server.withCookie \"sky_sid\" \"\" \"Path=/; HttpOnly; SameSite=Lax; Max-Age=0\" (Server.json \"{}\"))\n\n\n",
        );
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
        let mut run_setup = if io.reads_whole_model && io.msg_args.is_empty() {
            // Req IS the whole model.
            "                m =\n                    p\n".to_string()
        } else if io.reads_whole_model {
            // Whole model PLUS Msg-arg fields — `p` carries the Msg args too
            // (build_wire appends them), so `p` is WIDER than `Model`. Select the
            // model fields back out into a `Model` record; the Msg args are read
            // separately via `p.<arg>` in the ctor application below.
            let sets = model_fields
                .iter()
                .enumerate()
                .map(|(i, f)| {
                    let sep = if i == 0 { "" } else { ", " };
                    format!("{sep}{} = p.{}", f.name, f.name)
                })
                .collect::<String>();
            format!("                m =\n                    {{ {sets} }}\n")
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
        // Fix 5 (Judge finding 5): re-apply the `App.withRequest` hook
        // (`spaOnRequest_`) to the model server-side BEFORE the guard and update
        // run. `m` above is built from the wire payload `p`, which the wasm
        // client can FORGE — so a guard or an update that reads an identity /
        // session field off `m` would trust client-supplied data. `spaOnRequest_
        // req m` overwrites those fields from the REAL request (cookies /
        // headers / the server session), exactly as Sky.Live derives them from
        // server session state. Only when the app declared `withRequest`; the
        // model the guard + update see is `guard_model`.
        let mut guard_model = if has_synth_on_request {
            run_setup
                .push_str("\n                ( mReq, _ ) =\n                    spaOnRequest_ req m\n");
            "mReq".to_string()
        } else {
            "m".to_string()
        };
        // STATELESS SIGNED SESSION read path: override every identity field on the
        // model the guard + update see with the VERIFIED-cookie value, seeded from
        // `init ()` when there is no valid cookie. This runs UNCONDITIONALLY (not
        // gated on withRequest), so a forged wire `session` is discarded on every
        // handler. Under reads_whole_model (`m = p`) the record update overrides
        // ONLY the identity field(s), leaving the client's app state (cart, page)
        // intact. `base` (the init value) is bound by `run_setup` in the read-set
        // shapes but NOT in the reads_whole_model shapes, so bind it here for those.
        if !session_proj.is_empty() {
            if io.reads_whole_model {
                run_setup
                    .push_str("\n                ( base, _ ) =\n                    init ()\n");
            }
            let sets = session_proj
                .iter()
                .enumerate()
                .map(|(i, p)| {
                    let sep = if i == 0 { "" } else { ", " };
                    format!(
                        "{sep}{0} = {1} req base.{0}",
                        p.name,
                        session_verify_name(&p.name)
                    )
                })
                .collect::<String>();
            run_setup.push_str(&format!(
                "\n                mAuth =\n                    {{ {guard_model} | {sets} }}\n"
            ));
            guard_model = "mAuth".to_string();
        }
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
        // Server-internal effect chaining: this branch returns a `Cmd.perform`
        // chain that the analysis proved is settle-able server-side. Bind the
        // returned command (not `_`), settle it to a fixpoint via
        // `spaChainSettle_`, and encode the write-set from the FINAL model —
        // which already carries the UNION over every server-internal continuation
        // arm (spa_partition::compute_server_chaining). Without this the returned
        // `Cmd.perform` would run nowhere and its write silently dropped.
        let is_chaining = !push_mode && chaining.contains(name);
        // PATTERN-2 (client-result perform): this branch RUNS its server task and
        // answers with the task RESULT (`result = spaRunPerform_ cmd`); the client
        // dispatches `ResultMsg result`. The response is the single `result` field
        // (the write-set is applied by the result Msg's own client arm), so the
        // write-set model-read below is skipped.
        let is_client_result = !push_mode && client_result.contains_key(name);
        // The model the response reads from: the chain's final model when
        // chaining, else the branch's own updated model.
        let result_model = if is_chaining { "mFinal" } else { "m2" };
        // The response value.
        let resp_val = if is_client_result {
            "{ result = result }".to_string()
        } else if io.writes_whole_model {
            result_model.to_string()
        } else if resp_field_names.is_empty() {
            "{}".to_string()
        } else {
            let sets = resp_field_names
                .iter()
                .enumerate()
                .map(|(i, f)| {
                    let sep = if i == 0 { "" } else { ", " };
                    format!("{sep}{f} = {result_model}.{f}")
                })
                .collect::<String>();
            format!("{{ {sets} }}")
        };
        // STATELESS SIGNED SESSION write path: when this branch ESTABLISHES an
        // identity field (its write-set intersects the projection), wrap the JSON
        // response in `signedResponse_ <model>`, which re-issues the signed
        // `sky_sid` cookie from the model the branch produced. Only such a branch
        // emits a Set-Cookie; a branch that touches no identity field answers
        // plain. The wrap is applied at the single `Server.json` site so every
        // answer variant (plain / push / chaining / client-result) carries it.
        let establishes_session = !session_proj.is_empty()
            && (io.writes_whole_model
                || io
                    .write_fields
                    .iter()
                    .any(|f| session_proj.iter().any(|p| &p.name == f)));
        let json_resp = if establishes_session {
            format!(
                "signedResponse_ {result_model} (Server.json (Codec.toJson {resp_codec} {resp_val}))"
            )
        } else {
            format!("Server.json (Codec.toJson {resp_codec} {resp_val})")
        };
        // In push mode the returned Cmd is fed to the broker (a Cmd.publish fans
        // out to SSE subscribers) BEFORE the RPC answers; a chaining branch OR a
        // client-result branch binds `cmd` (settle server-side / run the task);
        // otherwise it is discarded (`_`).
        let (cmd_binder, answer) = if push_mode {
            (
                "cmd",
                format!(
                    "spaInterpretPublish spaBroker cmd\n\
                     \x20                       |> Task.andThen (\\_ -> Task.succeed ({json_resp}))"
                ),
            )
        } else if is_chaining || is_client_result {
            ("cmd", format!("Task.succeed ({json_resp})"))
        } else {
            ("_", format!("Task.succeed ({json_resp})"))
        };
        // The extra binding threaded between the `update` call and the answer, at
        // the guard / no-guard variant's own indentation: `( mFinal, _ ) =
        // spaChainSettle_ …` for a chaining branch, or `result = spaRunPerform_
        // cmd` for a client-result branch.
        let extra_bind = |indent: &str| -> String {
            if is_chaining {
                format!("\n{indent}( mFinal, _ ) =\n{indent}    spaChainSettle_ m2 cmd update\n")
            } else if is_client_result {
                format!("\n{indent}result =\n{indent}    spaRunPerform_ cmd\n")
            } else {
                String::new()
            }
        };
        let chain_bind_noguard = extra_bind("                ");
        let chain_bind_guard = extra_bind("                        ");
        // Fix 5: enforce the server-side guard BEFORE `update` runs. `spaGuard_
        // <msg> m` returns `Err` to reject the message — the handler answers 403
        // and NEVER runs the effect. This is the trusted authorisation point: the
        // wasm client can forge any `/_rpc` call, so the guard cannot live only on
        // the client. When the app declares no guard, the update runs directly as
        // before. `Result.` is imported via `Sky.Core.Prelude` in the entry, and
        // the guard body is threaded verbatim from `App.withGuard`.
        let run_and_answer = if has_synth_guard {
            format!(
                "\x20           case spaGuard_ {ctor_app} {guard_model} of\n\
                 \x20               Err ge ->\n\
                 \x20                   Task.succeed (forbidden (Error.toString ge))\n\n\
                 \x20               Ok _ ->\n\
                 \x20                   let\n\
                 \x20                       ( m2, {cmd_binder} ) =\n\
                 \x20                           update {ctor_app} {guard_model}\n\
                 {chain_bind_guard}\
                 \x20                   in\n\
                 \x20                   {answer}\n"
            )
        } else {
            format!(
                "\x20           let\n\
                 \x20               ( m2, {cmd_binder} ) =\n\
                 \x20                   update {ctor_app} {guard_model}\n\
                 {chain_bind_noguard}\
                 \x20           in\n\
                 \x20           {answer}\n"
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
             \x20           in\n\
             {run_and_answer}\n\
             \x20       Err e ->\n\
             \x20           Task.succeed (badRequest (Error.toString e))\n\n\n"
        ));
        routes.push(format!("        , Server.api \"POST /_rpc/{name}\" {handler}"));
    }
    // STATELESS SIGNED SESSION: the framework sign-out endpoint clears `sky_sid`.
    // Registered only when the projection is non-empty (its handler is emitted
    // under the same guard above), so an app with no server-trusted session gains
    // no extra route.
    if !session_proj.is_empty() {
        routes.push(
            "        , Server.api \"POST /_rpc/__spaSignOut\" spaSignOutHandler".to_string(),
        );
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
        // Settle decisions (fix 2). `settle_init` settles init's GET-safe read
        // (design §4.2, unchanged gate). `settle_nav` fires the app's
        // `onNavigate page` for the resolved route and settles ITS reads, so
        // per-route data (not only init's path-independent read) reaches the
        // embedded `#sky-model`. `seed_req` seeds init's model from the real
        // request via `withRequest`. All three settle through `Spa_ssrSettle`,
        // which runs under the goroutine-local SSR-safe guard — a destructive
        // effect in any folded command self-suppresses (a GET never mutates),
        // so `settle_nav`/`seed_req` need no static allowlist proof.
        let settle_init = init_get_safe;
        let settle_nav = nav_ssr;
        let seed_req = has_synth_on_request;
        let ssr_guard = has_synth_guard && nav_ssr;
        if settle_init || settle_nav || seed_req {
            handlers.push_str(
                "-- Data-resolved SSR (design §4.2): settle a GET-safe read to a\n\
                 -- data-bearing model server-side so the first paint carries REAL\n\
                 -- content a crawler sees. Runs under the SSR-safe guard\n\
                 -- (runtime-go/rt/spa_ssr_safe.go): a destructive effect in the\n\
                 -- folded command self-suppresses — a GET never mutates.\n\
                 spaSsrSettle : model -> any -> any -> model\n\
                 spaSsrSettle =\n\
                 \x20   Ffi.kernel \"Spa_ssrSettle\"\n\n\n",
            );
        }
        // The request param is needed for route resolution (`req.path`), the
        // `withRequest` seed (`req`), AND the STATELESS SIGNED SESSION cookie read.
        let req_param = if has_synth_routes || seed_req || !session_proj.is_empty() {
            "req"
        } else {
            "_"
        };
        // Build the let-binding block. Bindings sit at 8 spaces, `in` at 4, body
        // at 4. The settle chain NESTS `spaSsrSettle` calls (no intermediate
        // bindings + no `Cmd.batch`), so the no-seed/no-nav common case emits the
        // exact same `resolved = spaSsrSettle routed cmd0 update` as before.
        let mut lets = String::new();
        let cmd0_bind = if settle_init { "cmd0" } else { "_" };
        lets.push_str(&format!(
            "        ( model0, {cmd0_bind} ) =\n            init ()\n\n"
        ));
        // STATELESS SIGNED SESSION SSR seed: apply the SAME verified-cookie
        // override to the seed model BEFORE route resolution, so the first paint
        // reflects the verified identity (not the empty `init` value). The rest of
        // the SSR chain starts from `modelAuth0_` instead of `model0`.
        let ssr_start = if session_proj.is_empty() {
            "model0".to_string()
        } else {
            let sets = session_proj
                .iter()
                .enumerate()
                .map(|(i, p)| {
                    let sep = if i == 0 { "" } else { ", " };
                    format!(
                        "{sep}{0} = {1} req model0.{0}",
                        p.name,
                        session_verify_name(&p.name)
                    )
                })
                .collect::<String>();
            lets.push_str(&format!(
                "        modelAuth0_ =\n            {{ model0 | {sets} }}\n\n"
            ));
            "modelAuth0_".to_string()
        };
        // withRequest: refine init's model from the real request (path / query /
        // cookies / headers), exactly as Sky.Live seeds a session at start. Fixes
        // the seed to `()`, so `init` stays portable while the request arrives
        // through this web-only channel.
        let seed_base = if seed_req {
            lets.push_str(&format!(
                "        ( modelSeeded_, cmdSeed_ ) =\n            spaOnRequest_ req {ssr_start}\n\n"
            ));
            "modelSeeded_".to_string()
        } else {
            ssr_start.clone()
        };
        // Resolve the request path to the route's page (sets model.page).
        let route_base = if has_synth_routes {
            lets.push_str(&format!(
                "        routed =\n            spaSsrResolveModel spaRoutes_ spaNotFound_ {seed_base} req.path\n\n"
            ));
            "routed".to_string()
        } else {
            seed_base.to_string()
        };
        // The settle chain over the routed model: settle init's read, then the
        // request-seed's read, by NESTING (each runs under the SSR-safe guard).
        let mut chain = route_base.clone();
        if settle_init {
            chain = format!("spaSsrSettle {chain} cmd0 update");
        }
        if seed_req {
            chain = if settle_init {
                format!("spaSsrSettle ({chain}) cmdSeed_ update")
            } else {
                format!("spaSsrSettle {chain} cmdSeed_ update")
            };
        }
        // onNavigate: fire `onNavigate page` for the resolved route, run it
        // through `update` to a (model, cmd), and settle that command — the
        // per-route data load. When a guard is present it authorises the
        // navigation FIRST: a denied navigation renders the pre-nav model with NO
        // protected data settled (never a server-side data leak) and never runs
        // the load. The guard is the TRUSTED server-side check (fix 5).
        if settle_nav {
            lets.push_str(&format!("        preNav_ =\n            {chain}\n\n"));
            lets.push_str(
                "        navMsg_ =\n            spaOnNavigate_ preNav_.page\n\n\
                 \x20       ( navModel_, navCmd_ ) =\n            update navMsg_ preNav_\n\n",
            );
            let settle_expr = "spaSsrSettle navModel_ navCmd_ update";
            if ssr_guard {
                lets.push_str(&format!(
                    "        resolved =\n            case spaGuard_ navMsg_ preNav_ of\n\
                     \x20               Err _ ->\n                    preNav_\n\n\
                     \x20               Ok _ ->\n                    {settle_expr}\n\n"
                ));
            } else {
                lets.push_str(&format!("        resolved =\n            {settle_expr}\n\n"));
            }
        } else {
            lets.push_str(&format!("        resolved =\n            {chain}\n\n"));
        }
        lets.push_str("        modelJson =\n            Codec.toJson (Codec.auto resolved) resolved\n");
        handlers.push_str(&format!(
            "-- Server-render the REQUESTED route's first paint (design §4.1/§4.2):\n\
             -- run init, seed it from the request (withRequest), resolve the path to\n\
             -- this route's page, settle init's read AND the route's onNavigate read\n\
             -- to a data-bearing model so a crawler sees REAL per-route content;\n\
             -- render head + body inside a `data-sky-ssr`-marked #app; embed the\n\
             -- resolved model as JSON (design §4.5) so the client boots from it\n\
             -- instead of re-running the effectful init. `Codec.auto` derives the\n\
             -- model codec from the value — it compiles for ANY model (an\n\
             -- unencodable field degrades the blob at runtime, never breaks the build).\n\
             ssrHandler : Handler\n\
             ssrHandler {req_param} =\n\
             \x20   let\n\
             {lets}\
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
            // Per-route SSR registration. The patterns are collected by the
            // caller across EVERY project module (spa_split::generate), so a
            // route table factored into a sibling (`App.withRoutes
            // Routes.routes`, its literals in `Routes.sky`) is covered — not
            // only literals inline in the synthesised `spaRoutes_` binding.
            // Each `GET <pat>` is a more-specific mux entry than
            // `Server.static "/"`, so asset GETs still fall through; a `:param`
            // segment is translated to Go's `{param}` by the runtime's
            // `colonToMuxPattern`, so a `routeParam "/blog/:slug"` resolves.
            for pat in ssr_route_patterns {
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
    //
    // When the backend SSRs (≥1 server branch) AND has a route table (so a
    // `spaNotFound_` page exists), the static catch-all becomes
    // `Server.staticNotFound … ssrHandler`: a genuinely-unmatched cold path
    // (no such asset) falls through to the SSR handler, which resolves the
    // unmatched path to the NotFound page (Spa_ssrResolveModel) and renders the
    // shell — exactly as Sky.Live does — instead of a bare file-server 404. A
    // request that maps to a REAL asset still serves the file, so wasm_exec.js /
    // main.<hash>.wasm are never shadowed.
    // The app's LIVE static dir (transparent carry of `App.web { static }`). A
    // Sky.Live app serves its static dir from disk at request time, so an image
    // an admin uploads at runtime (`File.writeFile "public/products/<uuid>"`) is
    // served immediately at `/static/products/<uuid>`. The SPA backend otherwise
    // serves only the build-time `../frontend/dist` snapshot, which cannot hold a
    // runtime write, so the upload 404s. Mount the app's own dir (relative to the
    // backend's cwd, where its runtime writes land — the same cwd `File.writeFile`
    // resolves against) at its own prefix, BEFORE the `/` catch-all. The Go 1.22
    // mux matches the more-specific `/<prefix>/` ahead of `/`, so dist assets
    // (main.wasm, wasm_exec.js) are never shadowed. The declared static dir's
    // committed seed assets are staged into `backend/<dir>` at build
    // (`stage_declared_static_into_backend`), so seed + runtime uploads serve from
    // one place. Guarded: an empty prefix (the app mounts static at `/`) would
    // register a SECOND `/` handler and panic the mux, so it is skipped — the
    // dist catch-all already serves the root there.
    if let Some((dir, prefix)) = static_mount {
        if !prefix.is_empty() {
            routes.push(format!(
                "        , Server.static \"/{prefix}\" \"{dir}\""
            ));
        }
    }
    if emit_ssr && has_synth_routes {
        routes.push(
            "        , Server.staticNotFound \"/\" \"../frontend/dist\" ssrHandler".to_string(),
        );
    } else {
        routes.push("        , Server.static \"/\" \"../frontend/dist\"".to_string());
    }

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
    // GAP-1: mount the app's `App.api` server endpoints. `spaApiRoutes_` (the
    // synthesis' api route source) is lowered to `List Server.Route` by
    // `App.apiServerRoute` and appended to the static list literal via `++`, so
    // the api endpoints (OAuth callbacks, /healthz, webhooks) are served — the Go
    // 1.22 mux matches the more-specific `GET /path` ahead of the `/` static
    // catch-all regardless of registration order. Page routes are handled by the
    // per-route SSR GETs above; only api endpoints flow through here (a page route
    // yields `[]` from `apiServerRoute`).
    let listen_arg = if has_synth_api_routes {
        // Inside the grouping parens, layout is free, so the list literal keeps its
        // own indentation and the `++` appends the api mounts.
        format!("        (\n{route_block}\n        ]\n            ++ List.concatMap App.apiServerRoute spaApiRoutes_\n        )")
    } else {
        format!("{route_block}\n        ]")
    };
    // Force the captured boot setup BEFORE `Server.listen` when the synthesis
    // emitted a `spaBootSetup_` binding AND it reached the backend body (a
    // static-only backend copies no decls — line above — so it has no
    // `spaBootSetup_` to reference, and it does no server work that would need
    // the setup). `_ = spaBootSetup_` auto-forces the `Task Error ()`, running
    // the boot effects once at startup, exactly as the original `main`'s
    // `_ = <task>` prefix did.
    let main_decl = if has_synth_boot_setup && !static_only_backend {
        format!(
            "main : Task Error ()\nmain =\n    let\n        _ =\n            spaBootSetup_\n    in\n    Server.listen\n        serverPort\n{listen_arg}\n"
        )
    } else {
        format!(
            "main : Task Error ()\nmain =\n    Server.listen\n        serverPort\n{listen_arg}\n"
        )
    };
    handlers.push_str(&main_decl);

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
    // The `import Shared exposing (…)` clause for the entry (see gen_backend).
    shared_expose: &str,
    msg_param: &str,
    model_param: &str,
    update_anno: &str,
    model_ty: &str,
    backend_only: &HashSet<String>,
    // GAP-2: the init-command strip decision is resolved by the caller from
    // init's DECLARING module (entry or sibling). `init_strip` is the strip
    // decision, `init_in_entry` whether `init` is declared in the entry (only
    // then does gen_frontend strip it here — a sibling init is stripped in its
    // own module copy), and `init_pure_model` is init's pure model expr for the
    // client SSR model decoder (Some iff `init_strip`).
    init_strip: bool,
    init_in_entry: bool,
    init_pure_model: Option<&str>,
    // GAP-1: `update` is regenerated HERE (into the entry's frontend copy) only
    // when `update` is DECLARED in the entry. When it is factored into a sibling
    // module, its partitioned copy is regenerated in that module's own frontend
    // subset (render_module_client_subset) and the entry keeps its `import …
    // exposing (update)`, so gen_frontend must NOT append a second `update`.
    regen_update: bool,
    // Whether the app declared `App.withRpcError` (resolved from the entry).
    has_rpc_error: bool,
    // SERVER-INTERNAL Msgs — dropped from the `Msg` union + the client `update`.
    server_internal: &HashSet<String>,
    // Every Model field name (for the whole-model+Msg-arg request record).
    model_field_names: &[String],
    // PATTERN-2 (client-result perform): `root → info`. The root's `Applied<root>`
    // apply arm dispatches `info.result_msg resp.result` into `update`.
    client_result: &HashMap<String, ClientResultInfo>,
) -> Result<String, String> {
    // Imports: drop server-only effect modules AND any backend-only project
    // module (the security spine — an effectful module never reaches the client),
    // add Error + Shared.
    let mut import_lines: Vec<String> = imports
        .iter()
        .filter(|i| !is_server_only_module(&i.module_path) && !backend_only.contains(&i.module_path))
        // Same as the backend: a copied type/codec comes from `import Shared`, so
        // drop it from any kept import to avoid an ambiguous double-import.
        .map(|i| strip_names_from_import_exposing(&i.text, copied_names))
        .collect();
    if !has_module(imports, "Sky.Core.Error") {
        import_lines.push("import Sky.Core.Error as Error exposing (Error)".to_string());
    }
    import_lines.push(shared_expose.to_string());

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
    // The ENTRY init decl is stripped HERE only when `init` is declared in the
    // entry; a sibling-module init is stripped in its own module copy (the caller's
    // sibling-copy loop), so gen_frontend must not also try to strip a non-existent
    // entry `init` decl. The strip decision itself (`init_strip`) is the caller's,
    // computed from init's DECLARING module.
    let strip_entry_init = init_strip && init_in_entry;

    // Client model DECODER (design §4.5, blocker #1/#2). When `init`'s command is
    // stripped, the client boots from the SSR-embedded `#sky-model` blob instead
    // of re-running `init` — so it needs a `String -> Result Error model` decoder
    // symmetric with the backend's `Codec.toJson (Codec.auto model)` embed. Derive
    // it CLIENT-SIDE from `init`'s PURE model expression (which references no `db`,
    // so it is not server-tainted): `Codec.fromJson (Codec.auto <model>) json`.
    // Emitted into the frontend + wired onto the config's `main` here (not in the
    // App→Spa synthesis) so the decoder is never seen by the taint analysis as
    // reaching `db`. The pure model expr comes from init's DECLARING module (entry
    // or sibling), resolved by the caller, so a sibling init gets a decoder too.
    let decoder_blank = if init_strip {
        init_pure_model.map(|s| s.to_string())
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
            (Some("init"), DeclKind::Value) if strip_entry_init => {
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
                // Msg union: drop the SERVER-INTERNAL variants (their client arms
                // go too), then append the generated Applied<Msg> variants.
                body.push_str(&union_text_without_variants(src, &d, server_internal));
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
    //
    // The blank MUST be an explicitly-annotated top-level binding, not an inline
    // `Codec.auto ({..})` literal. In the heavily-constrained split-frontend module
    // an inline model literal type-checks to a STRUCTURAL row whose empty
    // collections / `Nothing` fields lower with their element type erased to
    // `[]any` / `Maybe any`; `Codec.auto` then reflects `kind interface`, which
    // `rt` cannot decode, so `fromJson` returns `Err` and the driver silently falls
    // back to `init` — dropping every nested-record collection on hydration with no
    // error (the SSR embed, derived from the runtime-typed model, carried the data
    // fine, so this is a silent SSR/client divergence). Pinning the blank to the
    // nominal `{model_ty}` via a top-level annotation makes codegen coerce those
    // fields to their declared element types, so the decoder's `Codec.auto` matches
    // the encoder's byte-for-byte and hydration is lossless.
    if let Some(model) = &decoder_blank {
        body.push_str(&format!(
            "spaModelBlank_ : {model_ty}\n\
             spaModelBlank_ =\n    \
             {model}\n\n\n\
             spaModelDecoder_ : String -> Result Error {model_ty}\n\
             spaModelDecoder_ jsonStr_ =\n    \
             Codec.fromJson (Codec.auto spaModelBlank_) jsonStr_\n\n\n"
        ));
    }

    // The regenerated update — ONLY when `update` is declared in the entry. A
    // sibling `update` is regenerated in its own module's frontend subset; the
    // entry keeps `import <Sibling> exposing (update)` and appends nothing.
    if regen_update {
        let update_src = gen_frontend_update(
            file, src, server, &server_ctors, msg_param, model_param, update_anno, has_rpc_error,
            server_internal, model_field_names, client_result,
        )?;
        body.push_str(&update_src);
        body.push_str("\n");
    }

    Ok(format!(
        "module Main exposing (main)\n\n-- Sky.Spa wasm CLIENT generated by `sky spa-split`. Pure branches run\n-- client-local (zero round-trip); each server branch goes through the explicit\n-- typed RPC boundary (Spa.postJson) using the SHARED codecs. Effectful\n-- (server-tainted) values/functions are NOT present in this source.\n\n{}\n\n\n{}",
        import_lines.join("\n"),
        body
    ))
}

/// Is `n` a synthesised App->Spa builder wrapper (`spaView_` / `spaHead_` /
/// `spaOnNavigate_` / …)? These are the names the `synthesize_spa_source` pass
/// (`sky/src/main.rs`) emits for the structural client entries when it derives a
/// `Spa.app` entry from an `App.app` / `App.web` value.
fn is_spa_builder_wrapper(n: &str) -> bool {
    n.len() > 4 && n.starts_with("spa") && n.ends_with('_')
}

/// Enforce the client-builder invariant on the generated frontend entry: EVERY
/// synthesised `spa*_` wrapper referenced in the client `Spa.config` / builder
/// chain MUST be defined in the frontend. A server-tainted wrapper is dropped by
/// `gen_frontend` (the security spine keeps effect-reaching code off the client),
/// which used to leave a DANGLING reference in the copied `main` -> the wasm build
/// failed with a bare `E1001 Undefined name: spaView_` pointing at generated code
/// the user never wrote. Resolve it by KIND:
///
///   * an OPTIONAL builder step (`|> Spa.with<Step> spaX_`) whose wrapper is
///     server-tainted is REMOVED from the client chain (the client loses that
///     step — e.g. a per-route `<head>` that reads `System.getenvOr` for a
///     canonical URL; the SSR backend still renders it), and
///   * the MANDATORY `view = spaView_` field cannot be dropped: a `--target
///     web:app` client view runs in the wasm client, which has no server
///     environment, so it must be PURE. A server-tainted view FAILS with an
///     actionable diagnostic naming the taint, never a dangling `spaView_`.
///
/// `taint_reason` maps a dropped wrapper to the report's taint reason (for the
/// view diagnostic). Stripped optional steps are appended to `notes`.
fn enforce_client_builder_invariant(
    frontend_src: &str,
    taint_reason: &HashMap<String, String>,
    notes: &mut Vec<String>,
) -> Result<String, String> {
    // Top-level `spa*_` wrappers that ARE defined in the frontend entry (a decl
    // head starts at column 0; the wrapper name is its first token).
    let mut defined: HashSet<String> = HashSet::new();
    for line in frontend_src.lines() {
        if line.starts_with(|c: char| c.is_whitespace()) || line.is_empty() {
            continue;
        }
        let tok = line
            .split(|c: char| c.is_whitespace() || c == '=')
            .next()
            .unwrap_or("");
        if is_spa_builder_wrapper(tok) {
            defined.insert(tok.to_string());
        }
    }

    let mut out = String::with_capacity(frontend_src.len());
    let mut stripped: Vec<String> = Vec::new();
    for line in frontend_src.lines() {
        let t = line.trim_start();
        // An optional builder step: `|> Spa.with<Step> <arg>`.
        if let Some(rest) = t.strip_prefix("|> Spa.") {
            let mut it = rest.split_whitespace();
            let step = it.next().unwrap_or("");
            let arg = it.next().unwrap_or("");
            if is_spa_builder_wrapper(arg) && !defined.contains(arg) {
                // Drop the step: its wrapper is server-tainted and was not carried
                // into the frontend. Never leave the dangling reference.
                stripped.push(format!("Spa.{step} (server-tainted `{arg}`)"));
                continue;
            }
        }
        // The mandatory `view` field: `, view = <arg>` (or `view = <arg>`).
        let view_ref = t
            .strip_prefix(", view =")
            .or_else(|| t.strip_prefix("view ="));
        if let Some(rest) = view_ref {
            let arg = rest.trim();
            if is_spa_builder_wrapper(arg) && !defined.contains(arg) {
                let reason = taint_reason
                    .get(arg)
                    .map(|r| format!(" ({r})"))
                    .unwrap_or_default();
                return Err(format!(
                    "the client SPA `view` is server-tainted: `{arg}`{reason}. \
A `--target web:app` client view runs in the wasm client, which has no server \
environment, so it must be PURE (no `System.getenvOr`, `Db`, `File`, `Http`, `Auth`, \
or other server effect). Move the environment/effect read out of the view into \
`init`/`update` (server-side), embed the value in the Model, and read it from the \
model in `view`."
                ));
            }
        }
        out.push_str(line);
        out.push('\n');
    }

    if !stripped.is_empty() {
        notes.push(format!(
            "client SPA: dropped server-tainted optional builder step(s) [{}] from the wasm client entry — the SSR backend still renders them, but they cannot run client-side (a client SPA head/step must be pure).",
            stripped.join(", ")
        ));
    }
    Ok(out)
}

fn gen_frontend_update(
    // The module that DECLARES `update` — the ENTRY, or a SIBLING module (GAP-1).
    // `update` is read + rewritten from THIS file/src.
    file: &SourceFile,
    src: &str,
    server: &[(String, BranchIo)],
    server_ctors: &[&str],
    msg_param: &str,
    model_param: &str,
    update_anno: &str,
    // Whether the app declared `App.withRpcError` — resolved by the caller from
    // the ENTRY (the synthesised `spaRpcError_` binding lives in the entry, so
    // the lookup stays against it even when `update` lives in a sibling module).
    has_rpc_error: bool,
    // SERVER-INTERNAL Msgs — their client `update` arms are DROPPED (the whole
    // chain settles server-side in the triggering branch's RPC).
    server_internal: &HashSet<String>,
    // Every Model field NAME, in declaration order. Needed to build an explicit
    // whole-model request record for a `reads_whole_model` branch that ALSO binds
    // Msg args: bare `model` misses those args (the backend `Req` carries them),
    // so such a branch must send `{ f1 = model.f1, …, arg = arg }` instead.
    model_field_names: &[String],
    // PATTERN-2 (client-result perform): `root → info`. The root's `Applied<root>`
    // apply arm dispatches `info.result_msg resp.result` into `update` instead of
    // applying a write-set.
    client_result: &HashMap<String, ClientResultInfo>,
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

    // Item 4: when the app declared `App.withRpcError` (carried by the App→Spa
    // synthesis into a `spaRpcError_ : Error -> Msg` binding), route a failed RPC
    // INTO `update` via that constructor, so the app's own view can show the
    // error — parity with Sky.Live's `Cmd.perform task ToMsg` error arm. Absent
    // the hook, keep the loud-log floor (model kept, perform site reports). The
    // presence flag is resolved by the caller against the ENTRY (see the param).
    let mut arms_out = String::new();
    for arm in case.arms() {
        let pat = arm.pattern().map(|p| p.syntax().clone());
        let head = pat.as_ref().and_then(first_upper);
        // SERVER-INTERNAL arm: dispatched only server-side (its ctor was pruned
        // from the frontend Msg union), so drop its client arm entirely.
        if head
            .as_ref()
            .map(|h| server_internal.contains(h))
            .unwrap_or(false)
        {
            continue;
        }
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
            // Request payload. A `reads_whole_model` branch with NO Msg args sends
            // bare `model` (the backend `Req` IS the whole model). But a whole-model
            // branch that ALSO binds Msg args must NOT send bare `model` — the
            // backend `Req` carries the Msg-arg fields too (build_wire appends them),
            // and the handler reads `p.<arg>`; bare `model` has no such field, so the
            // decode fails with "record is missing field(s): <arg>". Build the
            // explicit record covering every model field PLUS each Msg arg.
            let payload = if io.reads_whole_model && io.msg_args.is_empty() {
                model_param.to_string()
            } else if io.reads_whole_model {
                let mut parts: Vec<String> = model_field_names
                    .iter()
                    .map(|f| format!("{f} = {model_param}.{f}"))
                    .collect();
                for a in &io.msg_args {
                    if !model_field_names.iter().any(|f| f == a) {
                        parts.push(format!("{a} = {a}"));
                    }
                }
                if parts.is_empty() {
                    "{}".to_string()
                } else {
                    format!("{{ {} }}", parts.join(", "))
                }
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
        // PATTERN-2 (client-result perform): the RPC answered with the task
        // RESULT (`resp.result : Result Error T`). DISPATCH the client result Msg
        // with the WHOLE result value into `update`, so its client arm runs in the
        // wasm client — never decompose the `Result` into Ok/Err binders.
        let apply = if let Some(cr) = client_result.get(m) {
            format!("            update ({} resp.result) {model_param}", cr.result_msg)
        } else if io.writes_whole_model {
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
        // The Err arm. When the app declared `App.withRpcError`, route the error
        // INTO `update` via `spaRpcError_ e` so the app's own view can show it
        // (item 4 — parity with Sky.Live's `Cmd.perform task ToMsg` error arm).
        // Otherwise keep the model (the write-set never applied): the failure is
        // still NOT swallowed silently — the client's perform choke point surfaces
        // every non-network RPC Err loudly (runtime-go spa_neterror.go /
        // live_wasm.go performTask) and a network Err arms the retry overlay — so
        // keeping the model is the correct floor, not a discard.
        let err_arm = if has_rpc_error {
            format!(
                "        Applied{m} (Err e) ->\n            -- item 4: route the failed RPC into the app's own update.\n            update (spaRpcError_ e) {model_param}\n\n"
            )
        } else {
            format!(
                "        Applied{m} (Err _) ->\n            -- transport error surfaced loudly by the client perform site\n            -- (runtime-go performTask); model kept (write-set did not apply).\n            ( {model_param}, Cmd.none )\n\n"
            )
        };
        arms_out.push_str(&format!(
            "        Applied{m} (Ok resp) ->\n{apply}\n\n{err_arm}"
        ));
    }

    Ok(format!(
        "{update_anno}\nupdate {msg_param} {model_param} =\n    case {msg_param} of\n{}",
        arms_out.trim_end()
    ))
}

/// GAP-1 + GAP-2: render a tainted (MIXED) module's FRONTEND subset — every
/// NON-tainted (client-safe) decl kept, every server-tainted decl DROPPED, and,
/// when this is the module that declares `update` / the `Msg` union, the
/// partitioned `update` regenerated + the `Applied<Msg>` RPC variants injected.
/// The backend keeps the FULL module; this renders the wasm-client half. Sound
/// by construction: taint propagates upward (a non-tainted def only ever
/// references other non-tainted defs), so the dropped set is closed under the
/// call graph and the emitted subset can never reach a server effect.
#[allow(clippy::too_many_arguments)]
fn render_module_client_subset(
    mfile: &SourceFile,
    msrc: &str,
    // The server-tainted binding names in THIS module (dropped from the subset).
    tainted_here: &HashSet<String>,
    // Types/codecs COPIED into `Shared` (dropped from the subset — they arrive
    // via `import Shared exposing (..)`; re-declaring them would be a duplicate /
    // an ambiguous double-import with `Shared`).
    copied_names: &HashSet<String>,
    server: &[(String, BranchIo)],
    server_ctors: &[&str],
    // This module declares `update` (regenerate its partitioned copy here).
    regen_update: bool,
    // This module declares the `Msg` union (inject the Applied<Msg> variants).
    inject_msg: bool,
    // The `import Shared exposing (…)` clause this subset needs (generated wire
    // names + any copied name it stripped from its own decls, minus names it
    // still reads from a surviving `exposing (..)` origin; plus `Name(..)` for a
    // referenced moved union `Shared` owns).
    shared_expose: &str,
    // The moved unions `Shared` owns (type name → words: type + constructors).
    // A subset that references one must import it from `Shared`, even when it
    // neither regenerates `update` nor strips a copied decl.
    moved_unions: &BTreeMap<String, Vec<String>>,
    msg_param: &str,
    model_param: &str,
    update_anno: &str,
    // Project modules with NO frontend copy — an import of one is dropped (it
    // would be `E1001` in the frontend tree).
    no_frontend: &HashSet<String>,
    // Whether the app declared `App.withRpcError` (resolved from the entry).
    has_rpc_error: bool,
    // SERVER-INTERNAL Msgs — dropped from the `Msg` union + the client `update`.
    server_internal: &HashSet<String>,
    // Every Model field name (for the whole-model+Msg-arg request record).
    model_field_names: &[String],
    // PATTERN-2 (client-result perform): `root → info` (threaded to the sibling
    // module's regenerated `update`).
    client_result: &HashMap<String, ClientResultInfo>,
) -> Result<String, String> {
    let want: HashSet<&str> = server.iter().map(|(n, _)| n.as_str()).collect();

    let mut body = String::new();
    let mut stripped_copied = false;
    for d in mfile.decls() {
        let name = decl_name(&d);
        let n = name.as_deref();
        // Drop server-tainted VALUE decls (both annotation + value) — the spine.
        if let Some(n) = n {
            if tainted_here.contains(n) {
                continue;
            }
            // Drop types/codecs copied into `Shared` — they arrive via `import
            // Shared exposing (..)`; keeping them here would be a duplicate.
            if copied_names.contains(n) {
                stripped_copied = true;
                continue;
            }
        }
        match (n, decl_kind(&d)) {
            (Some("update"), _) if regen_update => {
                // Regenerated below (both its annotation + value are dropped here).
            }
            (_, DeclKind::Union)
                if inject_msg
                    && union_variant_names(&d).iter().any(|v| want.contains(v.as_str())) =>
            {
                // GAP-A: splice the Applied<Msg> RPC-response variants into the
                // Msg union whose variants ARE the app's server branches. Drop
                // the SERVER-INTERNAL variants first (their client arms go too).
                body.push_str(&union_text_without_variants(msrc, &d, server_internal));
                for (m, _) in server {
                    body.push_str(&format!("\n    | Applied{m} (Result Error {m}Resp)"));
                }
                body.push_str("\n\n\n");
            }
            _ => {
                body.push_str(slice(msrc, d.syntax()).trim_end());
                body.push_str("\n\n\n");
            }
        }
    }
    if regen_update {
        let update_src = gen_frontend_update(
            mfile, msrc, server, server_ctors, msg_param, model_param, update_anno, has_rpc_error,
            server_internal, model_field_names, client_result,
        )?;
        body.push_str(&update_src);
        body.push_str("\n");
    }

    // Reassemble: the module header + top comments + original imports (kept from
    // the prefix, everything before the first decl), then the client-safe body.
    // `drop_dangling_sibling_imports` then removes the imports the drop left
    // dangling — a project module with no frontend copy (E1001), and any
    // server-only module no longer referenced after the tainted decls went — and
    // the `Std.Spa` / `Shared` / `Error` imports the regenerated `update` +
    // injected variants reference are ensured.
    let first_decl_start = mfile
        .decls()
        .next()
        .map(|d| usize::from(d.syntax().text_range().start()))
        .unwrap_or(msrc.len());
    let prefix = msrc[..first_decl_start].trim_end();
    let mut out = String::with_capacity(prefix.len() + body.len() + 8);
    out.push_str(prefix);
    out.push_str("\n\n\n");
    out.push_str(&body);

    let out = drop_dangling_sibling_imports(&out, no_frontend);
    let out = if regen_update {
        ensure_import_present(&out, "Std.Spa", "import Std.Spa as Spa")
    } else {
        out
    };
    // The subset needs `import Shared` when it references the wire codecs (a
    // regenerated `update` / injected `Applied<Msg>` variants) OR when a copied
    // type/codec was stripped from it (its surviving defs now read that name from
    // `Shared`) OR when it references a moved union `Shared` owns (its origin no
    // longer declares it).
    let references_moved = moved_unions
        .values()
        .any(|words| words.iter().any(|w| module_mentions_word(&out, w)));
    let out = if regen_update || inject_msg || stripped_copied || references_moved {
        ensure_import_present(&out, "Shared", shared_expose)
    } else {
        out
    };
    let out = if inject_msg {
        ensure_import_present(&out, "Sky.Core.Error", "import Sky.Core.Error exposing (Error)")
    } else {
        out
    };
    Ok(out)
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

/// The constructor names of a `type X = A | B | …` union decl (empty for a
/// non-union decl). Used to identify the RIGHT `Msg` union (the one whose
/// variants are the app's branches) when several modules declare a same-named
/// type.
fn union_variant_names(d: &syntax::ast::Decl) -> Vec<String> {
    if let syntax::ast::Decl::Union(u) = d {
        u.variants()
            .into_iter()
            .filter_map(|v| v.name().map(|t| t.text().to_string()))
            .collect()
    } else {
        Vec::new()
    }
}

/// The declared argument types of a named union variant (`Saved (Result Error
/// String)` → `[Result Error String]`), or `None` when `d` is not a union or has
/// no variant named `name`.
fn union_variant_arg_types(d: &syntax::ast::Decl, name: &str) -> Option<Vec<ty::Ty>> {
    if let syntax::ast::Decl::Union(u) = d {
        for v in u.variants() {
            if v.name().map(|t| t.text() == name).unwrap_or(false) {
                return Some(ty::variant_arg_types(v.syntax()));
            }
        }
    }
    None
}

/// PATTERN-2: build the `root → ClientResultInfo` map from the partition report's
/// `(root, result_msg)` pairs. The result type is the result Msg's single
/// union-variant argument (`Result Error T`), read from whichever project module
/// declares the `Msg` union. A pair whose type cannot be recovered is DROPPED
/// (fail closed): the root then stays a plain wire branch everywhere, because
/// every split-side consumer reads THIS map.
fn build_client_result_map(
    db: &SkyDatabase,
    check_ids: &[ModuleId],
    pairs: &[(String, String)],
) -> HashMap<String, ClientResultInfo> {
    let mut out: HashMap<String, ClientResultInfo> = HashMap::new();
    for (root, result_msg) in pairs {
        let mut result_ty: Option<ty::Ty> = None;
        'mods: for m in check_ids {
            let parse = db.module_parse(*m);
            for d in parse.tree().decls() {
                if let Some(args) = union_variant_arg_types(&d, result_msg) {
                    result_ty = args.into_iter().next();
                    break 'mods;
                }
            }
        }
        if let Some(result_ty) = result_ty {
            out.insert(
                root.clone(),
                ClientResultInfo { result_msg: result_msg.clone(), result_ty },
            );
        }
    }
    out
}

/// Render a union declaration's source with the named `drop` variants REMOVED,
/// rebuilding the `= v1 | v2 | …` list so the result is still a well-formed
/// union (the first kept variant takes the `=`). Used to prune the
/// SERVER-INTERNAL Msgs from the frontend's `Msg` union (their client arms are
/// dropped too, so keeping the variant would leave the `case` non-exhaustive).
/// Returns the union verbatim when nothing is dropped or every variant would be
/// removed (fail-safe — never emits an empty union).
fn union_text_without_variants(src: &str, d: &syntax::ast::Decl, drop: &HashSet<String>) -> String {
    let syntax::ast::Decl::Union(u) = d else {
        return slice(src, d.syntax()).trim_end().to_string();
    };
    let verbatim = || slice(src, d.syntax()).trim_end().to_string();
    let variants = u.variants();
    if variants.is_empty() {
        return verbatim();
    }
    let kept: Vec<String> = variants
        .iter()
        .filter(|v| v.name().map(|t| !drop.contains(t.text())).unwrap_or(true))
        .map(|v| slice(src, v.syntax()).trim().to_string())
        .collect();
    if kept.is_empty() || kept.len() == variants.len() {
        // Nothing to drop, or dropping all — keep verbatim (fail-safe).
        return verbatim();
    }
    // Header = the source from the decl start up to the first variant, minus the
    // trailing `=` and whitespace (`type Msg`, `type Foo a b`).
    let decl_start = usize::from(d.syntax().text_range().start());
    let first_var_start = usize::from(variants[0].syntax().text_range().start());
    let header = src[decl_start..first_var_start]
        .trim_end()
        .trim_end_matches('=')
        .trim_end();
    format!("{header}\n    = {}", kept.join("\n    | "))
}

/// Splice the generated `Applied<Msg>` RPC-response variants into the `Msg` union
/// of a sibling module's source (GAP-A), returning the module source with the
/// variants appended to the union, any MOVED UNION declaration stripped (`Shared`
/// owns its single definition), and the `Shared` (wire types) + `Error` imports
/// ensured. Returns the source unchanged when there are no server branches or the
/// named union is absent (fail-safe — never a corrupting edit).
///
/// The module is rebuilt from its decls (a moved union is dropped, the `Msg`
/// union gains the `Applied<Msg>` variants, every other decl is kept verbatim) —
/// a reassembly, not an in-place splice, so a dropped decl anywhere in the module
/// is removed cleanly regardless of its position relative to `Msg`.
fn inject_applied_variants_into_module(
    mfile: &SourceFile,
    msrc: &str,
    server: &[(String, BranchIo)],
    // The `import Shared exposing (…)` clause this module needs — the generated
    // `Applied<Msg>` payload types PLUS `Name(..)` for any moved union it
    // references. The module KEEPS its own STRUCTURAL wire decls (its `exposing
    // (..)` consumers read them here), so those copied names are not re-imported.
    shared_expose: &str,
    // The moved unions `Shared` owns (type name → words). A declaration of one is
    // stripped from this copy; a reference to one is served by `shared_expose`.
    moved_unions: &BTreeMap<String, Vec<String>>,
    // SERVER-INTERNAL Msgs — pruned from the `Msg` union (their client arms go too).
    server_internal: &HashSet<String>,
) -> String {
    if server.is_empty() {
        return msrc.to_string();
    }
    // The `Msg` union is the one whose variants include the server branch ctors
    // (matched the same way the module was selected) — robust to the type's name.
    let want: HashSet<&str> = server.iter().map(|(n, _)| n.as_str()).collect();
    let has_msg_union = mfile.decls().any(|d| {
        matches!(decl_kind(&d), DeclKind::Union)
            && union_variant_names(&d).iter().any(|v| want.contains(v.as_str()))
    });
    if !has_msg_union {
        return msrc.to_string();
    }
    let first_decl_start = mfile
        .decls()
        .next()
        .map(|d| usize::from(d.syntax().text_range().start()))
        .unwrap_or(msrc.len());
    let prefix = msrc[..first_decl_start].trim_end();
    let mut body = String::new();
    for d in mfile.decls() {
        // Strip a moved union — `Shared` owns the single definition; keeping a
        // second copy here would be a distinct nominal type that never unifies.
        if let Some(n) = decl_name(&d) {
            if moved_unions.contains_key(&n) {
                continue;
            }
        }
        if matches!(decl_kind(&d), DeclKind::Union)
            && union_variant_names(&d).iter().any(|v| want.contains(v.as_str()))
        {
            body.push_str(&union_text_without_variants(msrc, &d, server_internal));
            for (m, _) in server {
                body.push_str(&format!("\n    | Applied{m} (Result Error {m}Resp)"));
            }
            body.push_str("\n\n\n");
        } else {
            body.push_str(slice(msrc, d.syntax()).trim_end());
            body.push_str("\n\n\n");
        }
    }
    let mut out = String::with_capacity(prefix.len() + body.len() + 8);
    out.push_str(prefix);
    out.push_str("\n\n\n");
    out.push_str(&body);
    let out = ensure_import_present(&out, "Shared", shared_expose);
    ensure_import_present(&out, "Sky.Core.Error", "import Sky.Core.Error exposing (Error)")
}

/// Ensure `import_line` is present in `src` (idempotent — no-op if `module_path`
/// is already imported under any alias/exposing form), inserting it after the
/// last existing `import …` line (else after the module declaration).
fn ensure_import_present(src: &str, module_path: &str, import_line: &str) -> String {
    let lines: Vec<&str> = src.lines().collect();
    let mut last_import: Option<usize> = None;
    for (i, l) in lines.iter().enumerate() {
        let t = l.trim_start();
        if let Some(rest) = t.strip_prefix("import ") {
            last_import = Some(i);
            if rest.trim_start().split_whitespace().next() == Some(module_path) {
                return src.to_string();
            }
        }
    }
    let mut out = String::with_capacity(src.len() + import_line.len() + 1);
    match last_import {
        Some(li) => {
            for (i, l) in lines.iter().enumerate() {
                out.push_str(l);
                out.push('\n');
                if i == li {
                    out.push_str(import_line);
                    out.push('\n');
                }
            }
        }
        None => {
            out.push_str(import_line);
            out.push('\n');
            out.push_str(src);
        }
    }
    out
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
    // `view : <Model> -> …` — the model is the FIRST parameter.
    if let Some(anno) = decl_text_by(file, src, "view", DeclKind::TypeAnno) {
        if let Some(m) = nth_arrow_segment(&anno, 0) {
            return Some(m);
        }
    }
    // `update : Msg -> <Model> -> ( Model, Cmd Msg )` — the model is the SECOND
    // parameter (the first is the `Msg`). Taking the first segment here — as the
    // old code did for both annotations — wrongly yielded `Msg` when the app had
    // no `view` type annotation, so `spaModelBlank_`/`spaModelDecoder_` were
    // annotated `Msg` and the frontend leg failed to type-check.
    if let Some(anno) = decl_text_by(file, src, "update", DeclKind::TypeAnno) {
        if let Some(m) = nth_arrow_segment(&anno, 1) {
            return Some(m);
        }
    }
    None
}

/// The `n`th top-level (paren-aware) `->` segment of a type annotation, trimmed.
/// `nth_arrow_segment("view : Model -> Html Msg", 0)` = `Some("Model")`;
/// `nth_arrow_segment("update : Msg -> Model -> ( Model, Cmd Msg )", 1)` =
/// `Some("Model")`. Splits only at arrows OUTSIDE brackets, so a function-typed
/// parameter (`(a -> b) -> …`) does not mis-segment. Returns `None` for an empty
/// or absent segment.
fn nth_arrow_segment(anno: &str, n: usize) -> Option<String> {
    let rhs = anno.split_once(':')?.1;
    let mut segments: Vec<String> = Vec::new();
    let mut cur = String::new();
    let mut depth: i32 = 0;
    let bytes = rhs.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        let c = bytes[i] as char;
        match c {
            '(' | '[' | '{' => {
                depth += 1;
                cur.push(c);
            }
            ')' | ']' | '}' => {
                depth -= 1;
                cur.push(c);
            }
            '-' if depth == 0 && i + 1 < bytes.len() && bytes[i + 1] == b'>' => {
                segments.push(cur.trim().to_string());
                cur.clear();
                i += 2;
                continue;
            }
            _ => cur.push(c),
        }
        i += 1;
    }
    segments.push(cur.trim().to_string());
    let seg = segments.get(n)?.trim().to_string();
    if seg.is_empty() {
        None
    } else {
        Some(seg)
    }
}

#[cfg(test)]
mod fix7_tests {
    use super::*;

    // `model_type_name` derives the Model type for `spaModelBlank_ : <Model>` from
    // the `view`/`update` annotation. It used to take the FIRST `->` segment of
    // whichever it found, which is the Model for `view : Model -> …` but the `Msg`
    // for `update : Msg -> Model -> …`. An app with no `view` type annotation fell
    // to `update` and got `Msg` — `spaModelBlank_ : Msg` failed to type-check.
    #[test]
    fn nth_arrow_segment_picks_the_right_parameter() {
        // view: model is the first param.
        assert_eq!(nth_arrow_segment("view : Model -> Html Msg", 0).as_deref(), Some("Model"));
        // update: model is the SECOND param (the first is Msg).
        assert_eq!(
            nth_arrow_segment("update : Msg -> Model -> ( Model, Cmd Msg )", 1).as_deref(),
            Some("Model")
        );
        // A function-typed first parameter must not mis-segment the arrows.
        assert_eq!(
            nth_arrow_segment("update : Msg -> AppModel -> ( AppModel, Cmd Msg )", 1).as_deref(),
            Some("AppModel")
        );
        assert_eq!(
            nth_arrow_segment("fold : (a -> b -> a) -> a -> List b -> a", 1).as_deref(),
            Some("a")
        );
        // Out-of-range / empty segment yields None.
        assert_eq!(nth_arrow_segment("x : Int", 1), None);
    }

    fn field(name: &str, ty_name: &str, ty: Option<ty::Ty>) -> ModelFieldTy {
        ModelFieldTy {
            name: name.to_string(),
            ty_name: ty_name.to_string(),
            codec: None,
            ty,
        }
    }

    // Fix 7 — the SSR model embed round-trips the whole model through
    // `Codec.auto`. Two shapes provably cannot survive it and MUST be flagged at
    // build time, ANYWHERE in a field's type (top level or nested):
    //   * `Secret` — rt.Secret redacts itself in every JSON path.
    //   * `Set a` — no goty arm, so it erases to `any`; the decode side has no
    //     `Set` arm and errors ("cannot decode kind interface").
    // Types the runtime codec DOES round-trip (Int / String / List / Maybe /
    // Money / Decimal / Dict, per codec_auto_test.go) must NOT be flagged — that
    // would be a false positive on a working app.
    //
    // RED before the fix: the detector matched only a TOP-LEVEL `Secret` tail, so
    // a `Set` field and a nested `Secret` (`Maybe Secret`, a record field) both
    // slipped through and degraded silently at runtime.
    #[test]
    fn secret_and_set_are_flagged_others_are_not() {
        let flagged = |f: &ModelFieldTy| codec_auto_unencodable(f).is_some();
        let reason = |f: &ModelFieldTy| codec_auto_unencodable(f).map(|(_, why)| why).unwrap_or_default();

        // Secret — resolved folded name, bare name, and surface fallback.
        assert!(flagged(&field("token", "Secret", Some(ty::Ty::app("Sky.Core.Secret.Secret", vec![])))));
        assert!(flagged(&field("token", "Secret", Some(ty::Ty::app("Secret", vec![])))));
        assert!(flagged(&field("token", "Secret", None)));

        // Set — top-level and via the surface fallback. Judge finding 2.
        let set = field("tags", "Set String", Some(ty::Ty::app("Set", vec![ty::Ty::app("String", vec![])])));
        assert!(flagged(&set));
        assert!(reason(&set).contains("Set"), "Set reason must name Set: {}", reason(&set));
        assert!(flagged(&field("tags", "Set String", None)));

        // Nested Secret — inside Maybe, inside List, inside a record field.
        assert!(flagged(&field("t", "Maybe Secret", Some(ty::Ty::app("Maybe", vec![ty::Ty::app("Secret", vec![])])))));
        assert!(flagged(&field("t", "List Secret", Some(ty::Ty::app("List", vec![ty::Ty::app("Secret", vec![])])))));
        assert!(flagged(&field(
            "cfg",
            "{ key : Secret }",
            Some(ty::Ty::Record(vec![(base::Name::new("key"), ty::Ty::app("Secret", vec![]))], None)),
        )));

        // Nested Set — inside Maybe.
        assert!(flagged(&field("m", "Maybe (Set Int)", Some(ty::Ty::app("Maybe", vec![ty::Ty::app("Set", vec![ty::Ty::app("Int", vec![])])])))));

        // Types the runtime codec round-trips must NOT be flagged.
        for (ty_name, t) in [
            ("Int", ty::Ty::app("Int", vec![])),
            ("String", ty::Ty::app("String", vec![])),
            ("Money", ty::Ty::app("Std.Money.Money", vec![])),
            ("Decimal", ty::Ty::app("Std.Decimal.Decimal", vec![])),
            ("List Todo", ty::Ty::app("List", vec![ty::Ty::app("Todo", vec![])])),
            ("Maybe Int", ty::Ty::app("Maybe", vec![ty::Ty::app("Int", vec![])])),
            ("Dict String Int", ty::Ty::app("Dict", vec![ty::Ty::app("String", vec![]), ty::Ty::app("Int", vec![])])),
        ] {
            let f = field("f", ty_name, Some(t));
            assert!(
                !flagged(&f),
                "{ty_name} round-trips via Codec.auto and must not be flagged"
            );
        }
    }

    // Item 4 — the fast per-commit leg for the failed-RPC routing (the full
    // `sky build --target web:app` flow tests in crates/sky/tests/spa_target_flow.rs
    // are #[ignore]d for the T1 budget). With a `spaRpcError_` binding present,
    // the generated frontend `Applied<Msg> (Err e)` arm routes into `update`;
    // without it, it keeps the loud-log floor `( model, Cmd.none )`.
    //
    // RED before item 4: the Err arm was ALWAYS `( model, Cmd.none )`.
    fn gen_update_for(with_rpc_error: bool) -> String {
        let rpc = if with_rpc_error { "\n\nspaRpcError_ =\n    (\\e -> RpcFailed e)\n" } else { "\n" };
        let src = format!(
            "module Main exposing (main)\n\nupdate msg model =\n    case msg of\n        Increment ->\n            ( {{ model | count = model.count + 1 }}, Cmd.none )\n\n        Save ->\n            ( saved model, Cmd.none )\n{rpc}"
        );
        let leaked: &'static str = Box::leak(src.into_boxed_str());
        let file = syntax::parse(leaked, base::FileId(0)).tree();
        let server = vec![(
            "Save".to_string(),
            BranchIo {
                reads_whole_model: false,
                read_fields: vec![],
                msg_args: vec![],
                writes_whole_model: false,
                write_fields: vec![],
            },
        )];
        gen_frontend_update(
            &file,
            leaked,
            &server,
            &["Save"],
            "msg",
            "model",
            "update : Msg -> Model -> ( Model, Cmd Msg )",
            with_rpc_error,
            &HashSet::new(),
            &[],
            &HashMap::new(),
        )
        .expect("gen_frontend_update")
    }

    #[test]
    fn rpc_error_arm_routes_into_update() {
        let with = gen_update_for(true);
        assert!(
            with.contains("update (spaRpcError_ e) model"),
            "item 4: with withRpcError the Err arm must route into update:\n{with}"
        );
        assert!(
            !with.contains("AppliedSave (Err _) ->"),
            "item 4: with the handler the swallow floor must be gone:\n{with}"
        );

        let without = gen_update_for(false);
        assert!(
            without.contains("AppliedSave (Err _) ->") && without.contains("( model, Cmd.none )"),
            "item 4: without withRpcError the Err arm keeps the loud-log floor:\n{without}"
        );
        assert!(
            !without.contains("spaRpcError_"),
            "item 4: no handler means no spaRpcError_ reference:\n{without}"
        );
    }
}
