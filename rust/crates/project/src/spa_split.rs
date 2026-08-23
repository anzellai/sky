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
//! Scope handled fully: the single-entry-module skeleton (pure branch + one
//! effectful branch with field-precise read/write sets, primitive field types,
//! no Msg args). Deferred (noted, not silently mis-handled): Msg-arg-typed RPC
//! inputs, whole-model fallback records, multi-module apps, non-primitive field
//! types. See the `notes` on the returned report.

use crate::spa_partition::{self, BranchIo, ModelFieldTy, SpaPartitionReport};
use hir::SkyDb;
use std::path::Path;
use syntax::ast::{AstNode, SourceFile};
use syntax::SyntaxKind;

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

/// The `<Msg>Req` / `<Msg>Resp` field lists for one SERVER branch, resolved
/// against the typed Model fields. Returns `(req_fields, resp_fields)` where
/// each entry is `(name, ty_name, codec)`. `codec` `None` ⇒ unsupported type.
fn wire_fields(
    io: &BranchIo,
    model_fields: &[ModelFieldTy],
) -> (Vec<ModelFieldTy>, Vec<ModelFieldTy>) {
    let lookup = |name: &str| -> ModelFieldTy {
        model_fields
            .iter()
            .find(|f| f.name == name)
            .cloned()
            .unwrap_or(ModelFieldTy {
                name: name.to_string(),
                ty_name: "any".into(),
                codec: None,
            })
    };
    let req: Vec<ModelFieldTy> = if io.reads_whole_model {
        model_fields.to_vec()
    } else {
        io.read_fields.iter().map(|f| lookup(f)).collect()
    };
    let resp: Vec<ModelFieldTy> = if io.writes_whole_model {
        model_fields.to_vec()
    } else {
        io.write_fields.iter().map(|f| lookup(f)).collect()
    };
    (req, resp)
}

/// A `type alias <Name> = { … }` + its codec, rendered from a field list.
fn render_wire_type(name: &str, codec_name: &str, fields: &[ModelFieldTy]) -> String {
    let mut out = String::new();
    // The record type.
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
    // The codec.
    out.push_str(&format!("{codec_name} : Codec {name}\n{codec_name} =\n"));
    out.push_str(&format!("    Codec.object {name}\n"));
    for f in fields {
        let codec = f.codec.clone().unwrap_or_else(|| "Codec.string".into());
        out.push_str(&format!("        |> Codec.field \"{0}\" .{0} {1}\n", f.name, codec));
    }
    out.push_str("        |> Codec.buildObject\n");
    out
}

/// Build `shared/Shared.sky` from the SERVER branches.
fn gen_shared(server: &[(String, BranchIo)], model_fields: &[ModelFieldTy], notes: &mut Vec<String>) -> String {
    let mut exposing: Vec<String> = Vec::new();
    let mut bodies = String::new();
    for (name, io) in server {
        let req_ty = format!("{name}Req");
        let resp_ty = format!("{name}Resp");
        let req_codec = format!("{}ReqCodec", lower_first(name));
        let resp_codec = format!("{}RespCodec", lower_first(name));
        exposing.push(req_ty.clone());
        exposing.push(req_codec.clone());
        exposing.push(resp_ty.clone());
        exposing.push(resp_codec.clone());
        let (req_fields, resp_fields) = wire_fields(io, model_fields);
        for f in req_fields.iter().chain(resp_fields.iter()) {
            if f.codec.is_none() {
                notes.push(format!(
                    "field `{}` has an unsupported type `{}` — its codec is a placeholder; wire it by hand.",
                    f.name, f.ty_name
                ));
            }
        }
        bodies.push_str(&format!("-- | {name} RPC — request = read-set, response = write-set.\n"));
        bodies.push_str(&render_wire_type(&req_ty, &req_codec, &req_fields));
        bodies.push_str("\n\n");
        bodies.push_str(&render_wire_type(&resp_ty, &resp_codec, &resp_fields));
        bodies.push_str("\n\n");
    }
    let exposing_list = exposing
        .iter()
        .map(|s| format!("    , {s}"))
        .collect::<Vec<_>>()
        .join("\n")
        .replacen("    ,", "    (", 1);
    format!(
        "-- | Shared — the ONE RPC wire contract compiled into BOTH the Sky.Spa wasm\n\
         -- client and the native Sky.Http.Server backend. Generated by `sky spa-split`.\n\
         -- One type, one codec, one wire shape: change a field and BOTH stop compiling.\n\
         module Shared exposing\n{exposing_list}\n    )\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Codec as Codec exposing (Codec)\n\n\n\
         {bodies}"
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

fn sky_toml(name: &str) -> String {
    format!("name = \"{name}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
}

/// Generate the two projects. `out_dir` gets `shared/`, `backend/`, `frontend/`.
pub fn generate(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
    out_dir: &Path,
) -> Result<SpaSplitReport, String> {
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

    // SERVER branches, keyed by ctor name, with their RPC I/O.
    let mut server: Vec<(String, BranchIo)> = Vec::new();
    let mut client_names: Vec<String> = Vec::new();
    for b in &report.branches {
        let name = ctor_name(&b.msg).to_string();
        if b.server {
            let io = b.io.clone().ok_or_else(|| {
                format!("server branch `{name}` has no derived RPC I/O")
            })?;
            if !io.msg_args.is_empty() {
                notes.push(format!(
                    "branch `{name}` binds Msg args {:?}; typed Req fields for Msg args are DEFERRED (skeleton has none). The arg is passed through but its codec field is omitted.",
                    io.msg_args
                ));
            }
            server.push((name, io));
        } else {
            client_names.push(name);
        }
    }
    if server.is_empty() {
        notes.push("no SERVER branches — the frontend is fully client-local and the backend only serves static assets.".into());
    }

    // CST + source text of the entry module (single-module apps).
    if check_ids.len() > 1 {
        // App modules beyond the entry are not sliced; note it.
        let extra = check_ids
            .iter()
            .filter(|m| **m != entry)
            .map(|m| db.module_name(*m).to_string())
            .filter(|n| !n.starts_with("Sky.") && !n.starts_with("Std.") && n != "Shared")
            .collect::<Vec<_>>();
        if !extra.is_empty() {
            notes.push(format!(
                "multi-module app: only the entry module is split; extra app modules {extra:?} are NOT copied (deferred)."
            ));
        }
    }
    let parse = db.module_parse(entry);
    let src = parse.syntax().text().to_string();
    let file = parse.tree();

    let imports = collect_imports(&file, &src);

    // Tainted binding names → excluded from the frontend.
    let tainted_names: Vec<String> = report.tainted.iter().map(|t| t.name.clone()).collect();

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
    let shared_src = gen_shared(&server, &report.model_fields, &mut notes);
    let backend_src = gen_backend(&file, &src, &imports, &server, &report.model_fields)?;
    let frontend_src = gen_frontend(
        &file,
        &src,
        &imports,
        &server,
        &client_names,
        &tainted_names,
        &msg_param,
        &model_param,
        &update_anno,
        &model_ty,
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
    write("backend/sky.toml", &sky_toml(&format!("{proj_name}-backend")), &mut files)?;
    write("frontend/sky.toml", &sky_toml(&format!("{proj_name}-frontend")), &mut files)?;

    Ok(SpaSplitReport {
        out_dir: out_dir.to_string_lossy().to_string(),
        files,
        server_branches: server.iter().map(|(n, _)| n.clone()).collect(),
        client_branches: client_names,
        excluded: tainted_names,
        notes,
    })
}

// ---------------------------------------------------------------------------
// Backend generation — copy the app verbatim, swap `main`, append handlers.
// ---------------------------------------------------------------------------

fn gen_backend(
    file: &SourceFile,
    src: &str,
    imports: &[ImportInfo],
    server: &[(String, BranchIo)],
    model_fields: &[ModelFieldTy],
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
    import_lines.push("import Shared exposing (..)".to_string());

    // All decls except `main` (both its annotation and value), verbatim.
    let mut body = String::new();
    for d in file.decls() {
        if decl_name(&d).as_deref() == Some("main") {
            continue;
        }
        body.push_str(slice(src, d.syntax()).trim_end());
        body.push_str("\n\n\n");
    }

    // The generated handlers + serverPort + main.
    let mut handlers = String::new();
    let mut routes: Vec<String> = Vec::new();
    handlers.push_str("badRequest : String -> Response\nbadRequest msg =\n    Server.withStatus 400 (Server.text msg)\n\n\n");
    for (name, io) in server {
        let handler = format!("{}Handler", lower_first(name));
        let req_codec = format!("{}ReqCodec", lower_first(name));
        let resp_codec = format!("{}RespCodec", lower_first(name));
        let (_req_fields, resp_fields) = wire_fields(io, model_fields);

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
        } else if resp_fields.is_empty() {
            "{}".to_string()
        } else {
            let sets = resp_fields
                .iter()
                .enumerate()
                .map(|(i, f)| {
                    let sep = if i == 0 { "" } else { ", " };
                    format!("{sep}{0} = m2.{0}", f.name)
                })
                .collect::<String>();
            format!("{{ {sets} }}")
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
             \x20               ( m2, _ ) =\n\
             \x20                   update {ctor_app} m\n\
             \x20           in\n\
             \x20           Task.succeed (Server.json (Codec.toJson {resp_codec} {resp_val}))\n\n\
             \x20       Err e ->\n\
             \x20           Task.succeed (badRequest (Error.toString e))\n\n\n"
        ));
        routes.push(format!("        , Server.api \"POST /_rpc/{name}\" {handler}"));
    }

    // serverPort + main.
    let route_block = {
        let mut rs = routes.join("\n");
        if !rs.is_empty() {
            rs = rs.replacen("        ,", "        [", 1);
        } else {
            rs = "        [".to_string();
        }
        rs
    };
    handlers.push_str(
        "serverPort : Int\nserverPort =\n    case String.toInt (System.getenvOr \"PORT\" \"8971\") of\n        Just p ->\n            p\n\n        Nothing ->\n            8971\n\n\n",
    );
    handlers.push_str(&format!(
        "main : Task Error ()\nmain =\n    Server.listen\n        serverPort\n{route_block}\n        , Server.static \"/\" \"../frontend/dist\"\n        ]\n"
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
    msg_param: &str,
    model_param: &str,
    update_anno: &str,
    model_ty: &str,
) -> Result<String, String> {
    // Imports: drop server-only effect modules, add Error + Shared.
    let mut import_lines: Vec<String> = imports
        .iter()
        .filter(|i| !is_server_only_module(&i.module_path))
        .map(|i| i.text.clone())
        .collect();
    if !has_module(imports, "Sky.Core.Error") {
        import_lines.push("import Sky.Core.Error as Error exposing (Error)".to_string());
    }
    import_lines.push("import Shared exposing (..)".to_string());

    let server_ctors: Vec<&str> = server.iter().map(|(n, _)| n.as_str()).collect();

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
        }
        match (name, decl_kind(&d)) {
            (Some("main"), _) => {
                // Keep `main = Spa.app …` verbatim.
                body.push_str(slice(src, d.syntax()).trim_end());
                body.push_str("\n\n\n");
            }
            (Some("update"), _) => {
                // Regenerated below; skip both annotation + value.
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
