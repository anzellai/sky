//! `sky doc <Module>` — terminal documentation for one module (doc 10 §"sky
//! doc"). Bring-up scope: the terminal renderer only — resolve a module name to
//! its `.sky` source, parse it, and print the exported bindings with their type
//! signatures and leading `-- |` doc summaries. The `--serve` (bundled
//! Sky.Http.Server app) and `--tui` (Sky.Tui browser) variants from
//! `app/Main.hs`'s `runDoc` are deferred — they spawn a bundled Sky app that the
//! rust bring-up does not yet materialise.
//!
//! Module resolution mirrors the Haskell doc index's reach: the stdlib under
//! `sky-stdlib/` plus the project's own `src/`. A bare last segment (`List`)
//! resolves to the module whose header ends with `.List` (`Sky.Core.List`); a
//! full dotted name (`Sky.Core.List`) matches directly.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use base::FileId;
use syntax::ast::{AstNode, Decl};

/// Render the terminal doc page for `module_arg`, resolving against the stdlib
/// under `repo_root/sky-stdlib` and the project's `project_dir/src`. Returns the
/// formatted page, or `Err` with a user-facing message when the module can't be
/// found.
pub fn render_module(
    repo_root: &Path,
    project_dir: &Path,
    module_arg: &str,
) -> Result<String, String> {
    let Some(path) = resolve_module_file(repo_root, project_dir, module_arg) else {
        return Err(format!(
            "sky doc: no module named `{module_arg}` under sky-stdlib/ or src/.\n\
             Try `sky doc --list` to see every documented module."
        ));
    };
    let src = std::fs::read_to_string(&path)
        .map_err(|e| format!("sky doc: cannot read {}: {e}", path.display()))?;
    Ok(render_source(&src))
}

/// Render a static doc-site into `out_dir` for `sky doc --serve`: an
/// `index.html` module index, one `m/<name>.html` per module, and a
/// machine-readable `api/symbols.json`. The bundled `sky-doc-server` app
/// (`sky-bundled/doc`) serves these files verbatim off `SKY_DOC_DIR` — its
/// routes are `/` → `index.html`, `/m/:name` → `m/<name>.html`, and
/// `/api/symbols.json`. This mirrors the file-layout contract of the Haskell
/// `Sky.Doc.Render.renderToDir`; the page rendering reuses the same terminal
/// projection (`render_source`) wrapped in HTML so the content matches
/// `sky doc <Module>`.
pub fn render_doc_site(
    repo_root: &Path,
    project_dir: &Path,
    out_dir: &Path,
) -> std::io::Result<()> {
    let mut mods = collect_module_files(repo_root, project_dir);
    mods.sort_by(|a, b| a.0.cmp(&b.0));
    mods.dedup_by(|a, b| a.0 == b.0);

    std::fs::create_dir_all(out_dir.join("m"))?;
    std::fs::create_dir_all(out_dir.join("api"))?;

    // index.html — a module list linking to each per-module page.
    let mut index = String::new();
    index.push_str("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">");
    index.push_str("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">");
    index.push_str("<title>Sky API docs</title>");
    index.push_str("<style>body{font-family:system-ui,sans-serif;max-width:52rem;margin:2rem auto;padding:0 1rem;line-height:1.5}h1{font-size:1.5rem}ul{columns:2;list-style:none;padding:0}a{text-decoration:none;color:#0b6}a:hover{text-decoration:underline}</style>");
    index.push_str("</head><body><h1>Sky API documentation</h1><ul>");
    for (name, _) in &mods {
        index.push_str(&format!(
            "<li><a href=\"/m/{}\">{}</a></li>",
            html_escape(name),
            html_escape(name)
        ));
    }
    index.push_str("</ul></body></html>\n");
    std::fs::write(out_dir.join("index.html"), index)?;

    // Per-module pages + the symbols manifest.
    let mut symbols = String::from("[");
    let mut first = true;
    for (name, path) in &mods {
        let src = std::fs::read_to_string(path).unwrap_or_default();
        let page = render_source(&src);
        let html = format!(
            "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">\
             <meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\
             <title>{} — Sky docs</title>\
             <style>body{{font-family:system-ui,sans-serif;max-width:52rem;margin:2rem auto;padding:0 1rem}}pre{{white-space:pre-wrap;font-family:ui-monospace,monospace}}a{{color:#0b6}}</style>\
             </head><body><p><a href=\"/\">&larr; all modules</a></p><pre>{}</pre></body></html>\n",
            html_escape(name),
            html_escape(&page)
        );
        std::fs::write(out_dir.join("m").join(format!("{name}.html")), html)?;

        if !first {
            symbols.push(',');
        }
        first = false;
        symbols.push_str(&format!("{{\"module\":{}}}", json_string(name)));
    }
    symbols.push(']');
    std::fs::write(out_dir.join("api").join("symbols.json"), symbols)?;
    Ok(())
}

/// Minimal HTML-escape for text embedded in an element body.
fn html_escape(s: &str) -> String {
    s.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

/// Emit `s` as a JSON string literal (quotes + the mandatory escapes).
fn json_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// List every module name discoverable under the stdlib + project `src/`, one
/// per line, sorted, grouped under `── project ──` / `── stdlib ──` headers
/// (a module under the project's own `src/` is a project module; everything
/// under `sky-stdlib/` is stdlib). Backs `sky doc --list`.
pub fn list_modules(repo_root: &Path, project_dir: &Path) -> String {
    let src_root = project_dir.join("src");
    let mut project: Vec<String> = Vec::new();
    let mut stdlib: Vec<String> = Vec::new();
    for (name, path) in collect_module_files(repo_root, project_dir) {
        if path.starts_with(&src_root) {
            project.push(name);
        } else {
            stdlib.push(name);
        }
    }
    for v in [&mut project, &mut stdlib] {
        v.sort();
        v.dedup();
    }
    let mut out = String::new();
    if !project.is_empty() {
        out.push_str("── project ──\n");
        out.push_str(&project.join("\n"));
        out.push_str("\n\n");
    }
    out.push_str("── stdlib ──\n");
    out.push_str(&stdlib.join("\n"));
    out
}

/// Enumerate `(module_name, path)` for every `.sky` under `sky-stdlib/` and
/// `project_dir/src/`. The module name is taken from the file's `module` header.
fn collect_module_files(repo_root: &Path, project_dir: &Path) -> Vec<(String, PathBuf)> {
    let mut files = Vec::new();
    collect_sky(&repo_root.join("sky-stdlib"), &mut files);
    collect_sky(&project_dir.join("src"), &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        if let Some(name) = header_name(&src) {
            out.push((name, path));
        }
    }
    out
}

/// Resolve `module_arg` to a source file: a full dotted name matches the header
/// exactly; a bare segment matches a header ending with `.<arg>` (or equal).
fn resolve_module_file(repo_root: &Path, project_dir: &Path, module_arg: &str) -> Option<PathBuf> {
    let mut candidates: Vec<(String, PathBuf)> = collect_module_files(repo_root, project_dir);
    candidates.sort_by(|a, b| a.0.cmp(&b.0));
    // Exact full-name match first.
    if let Some((_, p)) = candidates.iter().find(|(n, _)| n == module_arg) {
        return Some(p.clone());
    }
    // Then a trailing-segment match (`List` → `Sky.Core.List`).
    let suffix = format!(".{module_arg}");
    candidates
        .into_iter()
        .find(|(n, _)| n.ends_with(&suffix))
        .map(|(_, p)| p)
}

/// The `module <Name> exposing …` header name, if the source declares one.
fn header_name(src: &str) -> Option<String> {
    let parse = syntax::parse(src, FileId(0));
    parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|n| !n.is_empty())
}

/// Format the terminal doc page from a module's source text.
fn render_source(src: &str) -> String {
    let parse = syntax::parse(src, FileId(0));
    let tree = parse.tree();
    let module_name = tree
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|n| !n.is_empty())
        .unwrap_or_else(|| "<anonymous module>".to_string());

    let exposed = exposing_set(src, &tree);
    let docs = doc_comments(src);

    // Collect declarations in source order, keyed by name so a value binding and
    // its type annotation collapse into one entry (the annotation wins for the
    // rendered signature).
    let mut sigs: BTreeMap<String, String> = BTreeMap::new();
    let mut value_names: Vec<String> = Vec::new();
    let mut types: Vec<String> = Vec::new();
    for decl in tree.decls() {
        match decl {
            Decl::TypeAnno(d) => {
                if let (Some(name), Some(ty)) = (d.name(), d.ty()) {
                    sigs.insert(
                        name.text().to_string(),
                        normalize_ws(&ty.syntax().text().to_string()),
                    );
                }
            }
            Decl::Value(d) => {
                if let Some(name) = d.name() {
                    value_names.push(name.text().to_string());
                }
            }
            Decl::Alias(d) => {
                if let Some(name) = d.name() {
                    types.push(format!("type alias {}", name.text()));
                }
            }
            Decl::Union(d) => {
                if let Some(name) = d.name() {
                    let variants: Vec<String> = d
                        .variants()
                        .iter()
                        .filter_map(|v| v.name().map(|t| t.text().to_string()))
                        .collect();
                    if variants.is_empty() {
                        types.push(format!("type {}", name.text()));
                    } else {
                        types.push(format!("type {} = {}", name.text(), variants.join(" | ")));
                    }
                }
            }
            _ => {}
        }
    }

    let is_exported = |name: &str| exposed.as_ref().map(|s| s.contains(name)).unwrap_or(true);

    let mut out = String::new();
    out.push_str(&module_name);
    out.push('\n');
    if let Some(summary) = docs.get("\0module") {
        out.push_str("  ");
        out.push_str(summary);
        out.push('\n');
    }
    out.push('\n');

    // Ordered, de-duplicated list of exported value bindings: annotated ones
    // first (with their signature), then any exported binding with no
    // annotation (name only).
    let mut seen = std::collections::BTreeSet::new();
    let mut value_lines: Vec<String> = Vec::new();
    for (name, sig) in &sigs {
        if !is_exported(name) || !seen.insert(name.clone()) {
            continue;
        }
        let mut line = format!("  {name} : {sig}");
        if let Some(doc) = docs.get(name) {
            line.push_str(&format!("\n      {doc}"));
        }
        value_lines.push(line);
    }
    for name in &value_names {
        if !is_exported(name) || !seen.insert(name.clone()) {
            continue;
        }
        let mut line = format!("  {name}");
        if let Some(doc) = docs.get(name) {
            line.push_str(&format!("\n      {doc}"));
        }
        value_lines.push(line);
    }

    if !types.is_empty() {
        out.push_str("Types\n");
        for t in &types {
            out.push_str(&format!("  {t}\n"));
        }
        out.push('\n');
    }
    if !value_lines.is_empty() {
        out.push_str("Values\n");
        out.push_str(&value_lines.join("\n"));
        out.push('\n');
    }
    out
}

/// The set of names in the module's `exposing (…)` list, or `None` when the
/// module exposes `(..)` (everything). Constructor `(..)` after a type name is
/// ignored — we key on the top-level exposed names only.
fn exposing_set(
    _src: &str,
    tree: &syntax::ast::SourceFile,
) -> Option<std::collections::BTreeSet<String>> {
    let exposing = tree.module_header()?.exposing()?;
    let text = exposing.syntax().text().to_string();
    if text.contains("..") && !text.contains("(..)") {
        // A bare `(..)` — whole-module export. (`Type(..)` contains "(.." but
        // also names the type, so only a standalone `..` means export-all.)
        return None;
    }
    // Guard the standalone-`..` case: split identifiers; if one of the raw
    // tokens is exactly `..`, treat as export-all.
    let mut names = std::collections::BTreeSet::new();
    let mut all = false;
    for tok in text.split(|c: char| !c.is_alphanumeric() && c != '_' && c != '.') {
        let t = tok.trim();
        if t == ".." {
            all = true;
        } else if !t.is_empty() && !t.contains('.') {
            names.insert(t.to_string());
        }
    }
    if all {
        None
    } else {
        Some(names)
    }
}

/// Map a binding name → its leading `-- |` doc summary (the first line of the
/// doc block, comment marker stripped). The special key `"\0module"` holds the
/// module-level doc block (the `-- |` above the `module` keyword). A doc block
/// documents the identifier that leads the first non-comment, non-blank line
/// after it.
fn doc_comments(src: &str) -> BTreeMap<String, String> {
    let mut map = BTreeMap::new();
    let lines: Vec<&str> = src.lines().collect();
    let mut i = 0;
    while i < lines.len() {
        let trimmed = lines[i].trim_start();
        if let Some(rest) = trimmed
            .strip_prefix("-- |")
            .or_else(|| trimmed.strip_prefix("--|"))
        {
            let summary = rest.trim().to_string();
            // Skip the rest of the comment block.
            let mut j = i + 1;
            while j < lines.len() && lines[j].trim_start().starts_with("--") {
                j += 1;
            }
            // Skip blank lines.
            while j < lines.len() && lines[j].trim().is_empty() {
                j += 1;
            }
            // Attach to the leading identifier of the next code line.
            if j < lines.len() {
                let code = lines[j].trim_start();
                if code.starts_with("module ") {
                    map.entry("\0module".to_string()).or_insert(summary);
                } else if let Some(name) = leading_ident(code) {
                    map.entry(name).or_insert(summary);
                }
            }
            i = j;
        } else {
            i += 1;
        }
    }
    map
}

/// The leading `[A-Za-z_][A-Za-z0-9_]*` identifier of a source line, if any.
fn leading_ident(line: &str) -> Option<String> {
    let mut chars = line.char_indices();
    let (_, first) = chars.next()?;
    if !(first.is_ascii_alphabetic() || first == '_') {
        return None;
    }
    let end = line
        .char_indices()
        .find(|(_, c)| !(c.is_ascii_alphanumeric() || *c == '_'))
        .map(|(idx, _)| idx)
        .unwrap_or(line.len());
    Some(line[..end].to_string())
}

/// Collapse internal whitespace runs (including newlines from a multi-line
/// signature) to single spaces, trimming the ends.
fn normalize_ws(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// Recursively collect `*.sky` files under `dir`, skipping generated trees.
fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<PathBuf> = match std::fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok().map(|e| e.path())).collect(),
        Err(_) => return,
    };
    entries.sort();
    for path in entries {
        let generated = path.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out") | Some("sky-out-rust") | Some(".skycache") | Some(".skydeps")
            )
        });
        if generated {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extracts_signatures_and_docs() {
        let src = "-- | A little module.\nmodule M exposing (add, Color)\n\n\
                   -- | `add a b` — sum.\nadd : Int -> Int -> Int\nadd a b = a + b\n\n\
                   type Color = Red | Green\n";
        let page = render_source(src);
        assert!(page.contains("add : Int -> Int -> Int"), "page:\n{page}");
        assert!(page.contains("sum."), "doc missing:\n{page}");
        assert!(
            page.contains("type Color = Red | Green"),
            "union missing:\n{page}"
        );
    }

    #[test]
    fn hides_unexposed_bindings() {
        let src = "module M exposing (pub)\n\npub : Int\npub = 1\n\nhelper : Int\nhelper = 2\n";
        let page = render_source(src);
        assert!(page.contains("pub : Int"));
        assert!(!page.contains("helper"), "unexposed helper leaked:\n{page}");
    }

    #[test]
    fn export_all_shows_everything() {
        let src = "module M exposing (..)\n\na : Int\na = 1\n\nb : Int\nb = 2\n";
        let page = render_source(src);
        assert!(page.contains("a : Int"));
        assert!(page.contains("b : Int"));
    }

    #[test]
    fn json_string_escapes() {
        assert_eq!(json_string("Sky.Core.List"), "\"Sky.Core.List\"");
        assert_eq!(json_string("a\"b\\c"), "\"a\\\"b\\\\c\"");
    }

    #[test]
    fn html_escape_neutralises_markup() {
        assert_eq!(html_escape("a < b & c > d"), "a &lt; b &amp; c &gt; d");
    }

    #[test]
    fn render_doc_site_writes_index_module_pages_and_symbols() {
        // A minimal fake repo: one stdlib module + one project src module.
        let root = std::env::temp_dir().join(format!("sky-docsite-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let stdlib = root.join("sky-stdlib").join("Sky").join("Core");
        std::fs::create_dir_all(&stdlib).unwrap();
        std::fs::write(
            stdlib.join("List.sky"),
            "module Sky.Core.List exposing (map)\n\nmap : (a -> b) -> List a -> List b\nmap f xs = xs\n",
        )
        .unwrap();
        let proj = root.join("proj");
        std::fs::create_dir_all(proj.join("src")).unwrap();
        std::fs::write(
            proj.join("src").join("App.sky"),
            "module App exposing (main)\n\nmain : Int\nmain = 0\n",
        )
        .unwrap();

        let out = root.join("out");
        render_doc_site(&root, &proj, &out).unwrap();

        let index = std::fs::read_to_string(out.join("index.html")).unwrap();
        assert!(
            index.contains("/m/Sky.Core.List"),
            "index links module:\n{index}"
        );
        assert!(
            index.contains("/m/App"),
            "index links project module:\n{index}"
        );

        let list_page = std::fs::read_to_string(out.join("m").join("Sky.Core.List.html")).unwrap();
        assert!(
            list_page.contains("map : (a -&gt; b)"),
            "per-module page:\n{list_page}"
        );

        let symbols = std::fs::read_to_string(out.join("api").join("symbols.json")).unwrap();
        assert!(symbols.starts_with('[') && symbols.ends_with(']'));
        assert!(
            symbols.contains("\"module\":\"Sky.Core.List\""),
            "symbols:\n{symbols}"
        );

        let _ = std::fs::remove_dir_all(&root);
    }
}
