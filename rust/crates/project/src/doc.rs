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
        // Not a .sky-source module — fall back to the bare kernel name-list for
        // any kernel-only module without a .sky file yet (keeps every stdlib
        // module queryable). Every documented kernel module now has a .sky
        // source (v0.19), so this is only a safety net.
        if let Some(page) = render_kernel_module(module_arg) {
            return Ok(page);
        }
        return Err(format!(
            "sky doc: no module named `{module_arg}` under sky-stdlib/ or src/.\n\
             Try `sky doc --list` to see every documented module."
        ));
    };
    let src = std::fs::read_to_string(&path)
        .map_err(|e| format!("sky doc: cannot read {}: {e}", path.display()))?;
    // Every stdlib module (including the former kernel-only Live/Tui/Jobs/Cli and
    // the dual Sky.Http.Server) now declares its full surface in its .sky source
    // as Ffi.kernel aliases, so `sky doc` renders from that ONE source — no
    // curated-registry append (v0.19 kernel-metadata unification).
    Ok(render_source(&src))
}

/// Kernel-only modules: `(full_name, pseudo)` from the kernel registry whose
/// full name has no `.sky` source file — the ones the file scan misses.
fn kernel_only_modules() -> Vec<(&'static str, &'static [&'static str])> {
    let mut out = Vec::new();
    let mut seen = std::collections::HashSet::new();
    for (full, pseudo) in hir::KERNEL_MODULES {
        if !seen.insert(*full) {
            continue;
        }
        if let Some(funcs) = hir::kernel_functions(pseudo) {
            if !funcs.is_empty() {
                out.push((*full, funcs));
            }
        }
    }
    out
}

/// Render a kernel module's page: its exported binding names, noting they're
/// runtime-provided. Matches by full name or trailing segment (`Live` →
/// `Std.Live`). Returns `None` when `module_arg` names no kernel module.
fn render_kernel_module(module_arg: &str) -> Option<String> {
    let suffix = format!(".{module_arg}");
    let (full, funcs) = kernel_only_modules().into_iter().find(|(full, _)| {
        *full == module_arg || full.ends_with(&suffix)
    })?;
    let mut out = format!("── {full} ──\n\n");
    out.push_str("Runtime-provided module (its bindings live in the Sky runtime,\n");
    out.push_str("not in Sky source). Exported bindings:\n\n");
    for f in funcs {
        out.push_str(&format!("  {f}\n"));
    }
    Some(out)
}

/// Render a static doc-site into `out_dir` for `sky doc --serve`: an
/// `index.html` module index (with a client-side search bar), one
/// `m/<name>.html` per module, and a machine-readable `api/symbols.json`. The
/// bundled `sky-doc-server` app (`sky-bundled/doc`) serves these files verbatim
/// off `SKY_DOC_DIR` — its routes are `/` → `index.html`, `/m/:name` →
/// `m/<name>.html`, and `/api/symbols.json`. The page rendering reuses the same
/// terminal projection (`render_source`) wrapped in HTML so the content matches
/// `sky doc <Module>`.
///
/// `api/symbols.json` is `{"entries":[{module,name,sig,bucket,summary}, …]}` —
/// ONE object per exported binding, in declaration order within a module (modules
/// in name order). This ONE shape serves both consumers: the Go TUI catalog
/// loader (`runtime-go/rt/doc_catalog.go`, reads `module`/`name`/`sig`/`bucket`)
/// and the `--serve` index's client-side search (reads `module`/`name`/`sig`/
/// `summary`). `bucket` is `stdlib`/`project`.
/// Render the API doc-site for `sky doc --serve` / `--tui` (the bundled doc
/// server serves `/`, `/m/<mod>`, `/api/` and refuses `..` paths, so links stay
/// root-absolute and the API index is `index.html`).
pub fn render_doc_site(
    repo_root: &Path,
    project_dir: &Path,
    out_dir: &Path,
) -> std::io::Result<()> {
    render_doc_site_mode(repo_root, project_dir, out_dir, false)
}

/// Render the API pages for the STATIC export site (`sky doc --export`): the API
/// index is `reference.html` (the root `index.html` is the hand-written
/// landing), links are relative (`m/<mod>.html`) so the site works on any base
/// path, and every page carries the shared top nav.
pub fn render_doc_site_export(
    repo_root: &Path,
    project_dir: &Path,
    out_dir: &Path,
) -> std::io::Result<()> {
    render_doc_site_mode(repo_root, project_dir, out_dir, true)
}

fn render_doc_site_mode(
    repo_root: &Path,
    project_dir: &Path,
    out_dir: &Path,
    export: bool,
) -> std::io::Result<()> {
    // STRICT: a stdlib module that is unreadable, header-less or unparseable
    // used to disappear from `api/symbols.json` while this returned Ok(()).
    // That silently shrank the API denominator. It is now a hard failure with
    // every offending file named (§5.3 "silence becomes an error").
    let mut mods = collect_module_sources(repo_root, project_dir).map_err(|(_, problems)| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            format!(
                "{} stdlib module(s) would have been silently dropped from the API \
                 denominator:\n  - {}",
                problems.len(),
                problems.join("\n  - ")
            ),
        )
    })?;
    mods.sort_by(|a, b| a.name.cmp(&b.name));
    mods.dedup_by(|a, b| a.name == b.name);

    std::fs::create_dir_all(out_dir.join("m"))?;
    std::fs::create_dir_all(out_dir.join("api"))?;

    // The API index: a search bar + the module list. `export` picks the filename
    // (reference.html vs index.html), the link style (relative vs root-absolute),
    // and whether the shared top nav is present.
    let mut index = String::new();
    index.push_str("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">");
    index.push_str("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">");
    index.push_str("<title>API reference — Sky</title>");
    index.push_str(INDEX_STYLE);
    index.push_str("</head><body>");
    if export {
        index.push_str(&topnav("", "reference"));
        index.push_str("<h1>API reference</h1>");
        index.push_str(
            "<p class=\"lede\">Every standard-library module, generated from source on \
             each build — always current. Search a name, or run <code>sky doc &lt;Module&gt;</code> \
             locally. New to Sky? Start with the <a href=\"learn/index.html\">tour</a>.</p>",
        );
    } else {
        index.push_str("<h1>Sky API documentation</h1>");
    }
    index.push_str(
        "<input type=\"search\" id=\"q\" \
         placeholder=\"Search modules and symbols…\" \
         autocomplete=\"off\" autocapitalize=\"off\" spellcheck=\"false\" autofocus>",
    );
    index.push_str("<ul id=\"modlist\">");
    for DocSource { name, .. } in &mods {
        let href = if export {
            format!("m/{}.html", html_escape(name))
        } else {
            format!("/m/{}", html_escape(name))
        };
        index.push_str(&format!("<li><a href=\"{}\">{}</a></li>", href, html_escape(name)));
    }
    index.push_str("</ul>");
    index.push_str("<div id=\"results\" style=\"display:none\"></div>");
    index.push_str(&search_script(export));
    index.push_str("</body></html>\n");
    let index_name = if export { "reference.html" } else { "index.html" };
    std::fs::write(out_dir.join(index_name), index)?;

    // Per-module pages + the per-symbol manifest. The manifest is shaped
    // `{"entries":[{module,name,sig,bucket,summary}]}` — the ONE format both
    // consumers read: the Go TUI catalog loader (`runtime-go/rt/doc_catalog.go`,
    // needs `module`/`name`/`sig`/`bucket`) and the `--serve` index's client-side
    // search (uses `module`/`name`/`sig`/`summary`; ignores `bucket`). `bucket`
    // is `stdlib` for a module under `sky-stdlib/`, else `project`.
    let stdlib_root = repo_root.join("sky-stdlib");
    let mut symbols = String::from("{\"entries\":[");
    let mut first = true;
    for DocSource { name, path, src } in &mods {
        // NOT re-read from disk: `read_to_string(path).unwrap_or_default()` here
        // degraded an unreadable file to an empty module (zero symbols, exit 0)
        // — the fifth silent-shrink path. The bytes come from the strict
        // enumeration above, which already proved the file readable.
        let page = render_source(src);
        let syms = module_symbols(src);
        let bucket = if path.starts_with(&stdlib_root) {
            "stdlib"
        } else {
            "project"
        };
        let (nav, backlink) = if export {
            (topnav("../", "reference"), "<a href=\"../reference.html\">&larr; all modules</a>")
        } else {
            (String::new(), "<a href=\"/\">&larr; all modules</a>")
        };
        let html = format!(
            "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">\
             <meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\
             <title>{0} — Sky docs</title>\
             <style>body{{font-family:system-ui,sans-serif;max-width:52rem;margin:0 auto;padding:0 1rem 3rem;line-height:1.5}}pre{{white-space:pre-wrap;font-family:ui-monospace,monospace}}pre span:target{{background:#dcfce7;border-radius:.2rem}}a{{color:#0b6}}{1}</style>\
             </head><body>{2}<p>{3}</p><h1 style=\"font-size:1.4rem\">{0}</h1><pre>{4}</pre></body></html>\n",
            html_escape(name),
            TOPNAV_STYLE,
            nav,
            backlink,
            render_pre_with_anchors(&page, &syms),
        );
        std::fs::write(out_dir.join("m").join(format!("{name}.html")), html)?;

        for s in &syms {
            if !first {
                symbols.push(',');
            }
            first = false;
            symbols.push_str("{\"module\":");
            symbols.push_str(&json_string(name));
            symbols.push_str(",\"name\":");
            symbols.push_str(&json_string(&s.name));
            symbols.push_str(",\"sig\":");
            symbols.push_str(&json_string(&s.signature));
            symbols.push_str(",\"bucket\":");
            symbols.push_str(&json_string(bucket));
            symbols.push_str(",\"summary\":");
            symbols.push_str(&json_string(&s.summary));
            symbols.push('}');
        }
    }
    symbols.push_str("]}");
    std::fs::write(out_dir.join("api").join("symbols.json"), symbols)?;
    Ok(())
}

/// The one site-wide top navigation bar, shared by every generated page
/// (landing, reference, per-module, guides, tour). `prefix` is the relative hop
/// to the site root (`""` at root, `"../"` one level deep). `active` bolds the
/// current section (`home` / `learn` / `guides` / `reference`).
fn topnav(prefix: &str, active: &str) -> String {
    let item = |slug: &str, href: &str, label: &str| -> String {
        if slug == active {
            format!("<a href=\"{prefix}{href}\" aria-current=\"page\"><b>{label}</b></a>")
        } else {
            format!("<a href=\"{prefix}{href}\">{label}</a>")
        }
    };
    format!(
        "<nav class=\"topnav\">{}{}{}{}\
         <a href=\"https://github.com/anzellai/sky\">GitHub</a></nav>",
        item("home", "index.html", "Home"),
        item("learn", "learn/index.html", "Learn"),
        item("guides", "guide/index.html", "Guides &amp; internals"),
        item("reference", "reference.html", "API reference"),
    )
}

/// Shared `<style>` rules for the top nav — reused by the API pages (which don't
/// pull in `GUIDE_STYLE`).
const TOPNAV_STYLE: &str = "nav.topnav{position:sticky;top:0;z-index:5;background:Canvas;border-bottom:1px solid #8884;padding:.7rem 0;margin:0 0 1.5rem;font-size:.95rem}nav.topnav a{color:#0b6;text-decoration:none;margin-right:.9rem}nav.topnav a:hover{text-decoration:underline}nav.topnav a[aria-current] b{color:inherit}";

/// Inline `<style>` for `reference.html` — extends the original system-ui /
/// `#0b6` green-link palette with search-input + results-list styling.
const INDEX_STYLE: &str = "<style>\
:root{color-scheme:light dark}\
body{font-family:system-ui,sans-serif;max-width:52rem;margin:0 auto;padding:0 1rem 3rem;line-height:1.5}\
nav.topnav{position:sticky;top:0;z-index:5;background:Canvas;border-bottom:1px solid #8884;padding:.7rem 0;margin:0 0 1.5rem;font-size:.95rem}\
nav.topnav a{color:#0b6;text-decoration:none;margin-right:.9rem}nav.topnav a:hover{text-decoration:underline}\
p.lede{color:#666;margin:.2rem 0 1.2rem}p.lede code{background:#8882;padding:.1rem .3rem;border-radius:.3rem;font-family:ui-monospace,monospace}\
h1{font-size:1.5rem}\
input[type=search]{width:100%;box-sizing:border-box;font:inherit;padding:.55rem .7rem;margin:0 0 1.25rem;border:1px solid #ccc;border-radius:.4rem}\
input[type=search]:focus{outline:none;border-color:#0b6;box-shadow:0 0 0 2px #0b64}\
ul{columns:2;list-style:none;padding:0}\
ul.res{columns:1}\
ul.res li{break-inside:avoid;margin:0 0 .65rem}\
a{text-decoration:none;color:#0b6}a:hover{text-decoration:underline}\
.mod{color:#888}.sig{color:#444}\
.sum{display:block;color:#666;font-size:.9rem;margin:.1rem 0 0}\
.empty{color:#888}\
b{font-weight:600}\
</style>";

/// Inline, dependency-free search script for the API index. Fetches the symbol
/// manifest once, then filters on every keystroke by module / name / signature
/// substring (case-insensitive) and renders matches as links into the per-module
/// pages. CSP-safe: no remote scripts, no `eval`. `export` picks relative
/// (`m/<mod>.html#`, `api/…`) vs root-absolute (`/m/<mod>#`, `/api/…`) URLs so
/// the same script works on the static Pages site and behind the serve server.
fn search_script(export: bool) -> String {
    let (mfetch, mprefix, msuffix) = if export {
        ("api/symbols.json", "m/", ".html#")
    } else {
        ("/api/symbols.json", "/m/", "#")
    };
    format!(
        r#"<script>
(function () {{
  var q = document.getElementById('q');
  var modlist = document.getElementById('modlist');
  var results = document.getElementById('results');
  var SYMS = [];
  function esc(s) {{
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }}
  function render() {{
    var query = q.value.trim().toLowerCase();
    if (!query) {{
      modlist.style.display = '';
      results.style.display = 'none';
      results.innerHTML = '';
      return;
    }}
    modlist.style.display = 'none';
    results.style.display = '';
    var hits = [];
    for (var i = 0; i < SYMS.length && hits.length < 300; i++) {{
      var s = SYMS[i];
      var hay = (s.module + '.' + s.name + ' ' + (s.sig || '')).toLowerCase();
      if (hay.indexOf(query) !== -1) hits.push(s);
    }}
    if (!hits.length) {{
      results.innerHTML = '<p class="empty">No matches for “' + esc(q.value) + '”.</p>';
      return;
    }}
    var html = '<ul class="res">';
    for (var j = 0; j < hits.length; j++) {{
      var h = hits[j];
      var sig = h.sig ? ' <span class="sig">: ' + esc(h.sig) + '</span>' : '';
      var sum = h.summary ? '<div class="sum">' + esc(h.summary) + '</div>' : '';
      html += '<li><a href="{mprefix}' + esc(h.module) + '{msuffix}' + esc(h.name) + '">'
        + '<span class="mod">' + esc(h.module) + '.</span><b>' + esc(h.name) + '</b>'
        + sig + '</a>' + sum + '</li>';
    }}
    html += '</ul>';
    results.innerHTML = html;
  }}
  q.addEventListener('input', render);
  fetch('{mfetch}')
    .then(function (r) {{ return r.json(); }})
    .then(function (d) {{ SYMS = (d && d.entries) || []; render(); }})
    .catch(function () {{}});
}})();
</script>"#
    )
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
    // Honour sky.toml `root` (default `src`) so a project whose sources live
    // under `lib/` still buckets its own modules under `── project ──`.
    let src_root = project_dir.join(crate::build::configured_source_root(project_dir));
    let mut project: Vec<String> = Vec::new();
    let mut stdlib: Vec<String> = Vec::new();
    for (name, path) in collect_module_files(repo_root, project_dir) {
        if path.starts_with(&src_root) {
            project.push(name);
        } else {
            stdlib.push(name);
        }
    }
    // Any remaining kernel-only stdlib module (from the kernel-function registry)
    // without a .sky file is added so every stdlib module is listed. All
    // documented kernel modules now have a .sky source (v0.19), so the file scan
    // already covers them; this is a safety net.
    let have: std::collections::HashSet<&str> = stdlib.iter().map(String::as_str).collect();
    let mut kernel_extra: std::collections::HashSet<String> = std::collections::HashSet::new();
    for (full, _) in kernel_only_modules() {
        if !have.contains(full) {
            kernel_extra.insert(full.to_string());
        }
    }
    stdlib.extend(kernel_extra);
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

/// A module file that was read successfully AND carries a `module` header.
/// Holding `src` is deliberate: the doc-site render used to re-read the file
/// with `read_to_string(path).unwrap_or_default()`, so a file that became
/// unreadable BETWEEN enumeration and render degraded to an empty module —
/// zero symbols, exit 0. Reading once and passing the bytes along removes that
/// path entirely (and the TOCTOU window with it).
struct DocSource {
    name: String,
    path: PathBuf,
    src: String,
}

/// Enumerate `(module_name, path)` for every `.sky` under `sky-stdlib/` and
/// `project_dir/src/`. The module name is taken from the file's `module` header.
///
/// LENIENT: a file that cannot be read, or has no header, is skipped. This is
/// the right behaviour for `sky doc <Module>` resolution and `sky doc --list`,
/// which must keep working while the user has a half-written file open. The
/// doc-SITE render uses [`collect_module_sources`] instead, which is strict —
/// see its docstring for why the split exists.
fn collect_module_files(repo_root: &Path, project_dir: &Path) -> Vec<(String, PathBuf)> {
    match collect_module_sources(repo_root, project_dir) {
        Ok(v) | Err((v, _)) => v.into_iter().map(|m| (m.name, m.path)).collect(),
    }
}

/// Enumerate every module file WITH its source, reporting — rather than
/// swallowing — every file that could not become a module.
///
/// # Why this is strict (docs/ci-test-architecture-v2.md §5.2, §5.3)
///
/// `api/symbols.json` is the stdlib API DENOMINATOR. Four separate paths used to
/// shrink that denominator while exiting 0, which makes "100 % of the stdlib is
/// covered" easier to claim by covering less:
///
/// 1. a file becomes unreadable → `let Ok(src) = … else { continue }` dropped it;
/// 2. a module loses its `module` header → `if let Some(name) = …` with no
///    `else` dropped it;
/// 3. a module stops PARSING → `header_name`/`module_symbols` called
///    `syntax::parse` and never once looked at `parse.errors()`, so a module with
///    a broken declaration silently contributed only the symbols the recovering
///    parser still managed to see;
/// 4. the render then re-read the file with `unwrap_or_default()`, degrading an
///    unreadable file to an EMPTY module rather than dropping it.
///
/// Every one of those is now an `Err`. Silence became an error.
///
/// # The one explicit, owned exemption
///
/// Strictness applies to files under `sky-stdlib/` — the shipped surface that IS
/// the denominator. Files under the user's project `src/` stay lenient: a
/// developer with a half-written module must still be able to run `sky doc`, and
/// their files are not part of the stdlib denominator (they carry
/// `"bucket":"project"`). This exemption is stated rather than hidden, per §5.5.
fn collect_module_sources(
    repo_root: &Path,
    project_dir: &Path,
) -> Result<Vec<DocSource>, (Vec<DocSource>, Vec<String>)> {
    let stdlib_root = repo_root.join("sky-stdlib");
    let mut files = Vec::new();
    collect_sky(&stdlib_root, &mut files);
    let stdlib_count = files.len();
    collect_sky(
        &project_dir.join(crate::build::configured_source_root(project_dir)),
        &mut files,
    );

    let mut out = Vec::new();
    let mut problems: Vec<String> = Vec::new();
    for (i, path) in files.into_iter().enumerate() {
        let strict = i < stdlib_count;
        let show = path.display().to_string();
        let src = match std::fs::read_to_string(&path) {
            Ok(s) => s,
            Err(e) => {
                if strict {
                    problems.push(format!("{show}: unreadable ({e})"));
                }
                continue;
            }
        };
        // Parse ONCE, here, and look at the errors — the check that never existed.
        let parse = syntax::parse(&src, FileId(0));
        let errors = parse.errors();
        if strict && !errors.is_empty() {
            let first = errors
                .first()
                .map(|d| d.message.clone())
                .unwrap_or_else(|| "parse error".to_string());
            problems.push(format!(
                "{show}: does not parse ({} error(s); first: {first})",
                errors.len()
            ));
            continue;
        }
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|n| !n.is_empty());
        match name {
            Some(name) => out.push(DocSource { name, path, src }),
            None => {
                if strict {
                    problems.push(format!(
                        "{show}: no `module <Name> exposing …` header — the module would \
                         vanish from the API denominator"
                    ));
                }
            }
        }
    }

    if problems.is_empty() {
        Ok(out)
    } else {
        Err((out, problems))
    }
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

// NOTE: the old free-standing `header_name(src)` is gone. It re-parsed the file
// purely to read the header and discarded `parse.errors()` in the process; the
// header is now read from the SAME parse that checks for errors, inside
// `collect_module_sources`, so a module can no longer be accepted by one parse
// and silently mis-served by another.

/// Format the terminal doc page from a module's source text.
/// A union variant rendered WITH its argument types (`Resend String`), from the
/// variant node's source text (whitespace-normalised). Rendering the name alone
/// dropped the args — `type EmailProvider = Resend | Ses` implied nullary
/// constructors while the checker treats `Resend` as `String -> EmailProvider`.
fn variant_text(v: &syntax::ast::UnionVariant) -> String {
    v.syntax()
        .text()
        .to_string()
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
}

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
                    let variants: Vec<String> = d.variants().iter().map(variant_text).collect();
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

/// One exported binding of a module, for the `api/symbols.json` manifest.
/// `signature` is the annotated type (or the `type …` / `type alias …` head for
/// types), empty when the binding carries no annotation. `summary` is the first
/// line of its `-- |` doc block, empty when undocumented.
struct DocSym {
    name: String,
    signature: String,
    summary: String,
}

/// Extract every EXPORTED binding of a module in declaration order — one entry
/// per name (a value and its type annotation collapse into a single entry).
/// Shares the parsing primitives (`exposing_set` / `doc_comments`) with the
/// terminal projection `render_source`, so the manifest and the pages stay in
/// step. Robust to unannotated modules: an exported binding with no signature
/// still appears (empty `signature`).
fn module_symbols(src: &str) -> Vec<DocSym> {
    module_symbols_with(src, true)
}

/// `module_symbols`, with the `exposing`-list filter switchable OFF.
///
/// The filter is the FIRST denominator-shrink path: narrowing a module's
/// `exposing (…)` list removes symbols from `api/symbols.json` and nothing
/// notices. It is legitimate for the published docs (the docs should show the
/// public API), so it is not an error — but it means the denominator has two
/// honest readings, and `xtask denominators` must report BOTH and never average
/// them: `apply_exposing = true` is the public API surface, `false` is every
/// top-level declaration the module contains.
fn module_symbols_with(src: &str, apply_exposing: bool) -> Vec<DocSym> {
    let parse = syntax::parse(src, FileId(0));
    let tree = parse.tree();
    let exposed = if apply_exposing { exposing_set(src, &tree) } else { None };
    let docs = doc_comments(src);
    let is_exported = |name: &str| exposed.as_ref().map(|s| s.contains(name)).unwrap_or(true);

    // First-seen declaration order + the signature for each name.
    let mut order: Vec<String> = Vec::new();
    let mut seen = std::collections::BTreeSet::new();
    let mut sigs: BTreeMap<String, String> = BTreeMap::new();
    let note =
        |name: String, order: &mut Vec<String>, seen: &mut std::collections::BTreeSet<String>| {
            if seen.insert(name.clone()) {
                order.push(name);
            }
        };
    for decl in tree.decls() {
        match decl {
            Decl::TypeAnno(d) => {
                if let (Some(name), Some(ty)) = (d.name(), d.ty()) {
                    let n = name.text().to_string();
                    sigs.insert(n.clone(), normalize_ws(&ty.syntax().text().to_string()));
                    note(n, &mut order, &mut seen);
                }
            }
            Decl::Value(d) => {
                if let Some(name) = d.name() {
                    note(name.text().to_string(), &mut order, &mut seen);
                }
            }
            Decl::Alias(d) => {
                if let Some(name) = d.name() {
                    let n = name.text().to_string();
                    sigs.insert(n.clone(), format!("type alias {}", name.text()));
                    note(n, &mut order, &mut seen);
                }
            }
            Decl::Union(d) => {
                if let Some(name) = d.name() {
                    let n = name.text().to_string();
                    let variants: Vec<String> = d.variants().iter().map(variant_text).collect();
                    let sig = if variants.is_empty() {
                        format!("type {}", name.text())
                    } else {
                        format!("type {} = {}", name.text(), variants.join(" | "))
                    };
                    sigs.insert(n.clone(), sig);
                    note(n, &mut order, &mut seen);
                }
            }
            _ => {}
        }
    }

    order
        .into_iter()
        .filter(|n| is_exported(n))
        .map(|n| DocSym {
            signature: sigs.get(&n).cloned().unwrap_or_default(),
            summary: docs.get(&n).cloned().unwrap_or_default(),
            name: n,
        })
        .collect()
}

/// HTML-escape the rendered terminal page for embedding in `<pre>`, giving each
/// top-level symbol line an `id` anchor so `/m/<mod>#<name>` fragment links
/// land on the binding. Only value/binding lines (whose first token IS the
/// symbol name) are anchored; type lines lead with `type` so they render plain.
/// One stdlib module's contribution to the API denominator, reported BOTH ways.
///
/// `filtered_*` is what `sky doc --export` publishes (the `exposing` list
/// applied); `unfiltered_*` is every top-level declaration in the file. For a
/// module whose header is `exposing (..)` the two are equal BY CONSTRUCTION —
/// there is no public-API curation to apply, so its contribution is "every
/// top-level declaration, including helpers never intended as API". Those
/// modules are flagged with `exposes_all` so the ledger can report them
/// separately instead of averaging two different kinds of number together.
#[derive(Clone, Debug)]
pub struct ModuleDenominator {
    pub module: String,
    /// The module header is `exposing (..)` — nothing is curated.
    pub exposes_all: bool,
    pub filtered_entries: usize,
    pub filtered_values: usize,
    pub filtered_types: usize,
    pub unfiltered_entries: usize,
    pub unfiltered_values: usize,
    pub unfiltered_types: usize,
}

/// Per-module stdlib denominators, computed from the SAME code path
/// `sky doc --export` uses to write `api/symbols.json` (`collect_module_sources`
/// + `module_symbols`), so the two can never disagree.
///
/// Fails — rather than returning a smaller list — if any stdlib module is
/// unreadable, header-less or unparseable.
pub fn stdlib_denominators(repo_root: &Path) -> std::io::Result<Vec<ModuleDenominator>> {
    let stdlib_root = repo_root.join("sky-stdlib");
    // `project_dir` = the stdlib root itself: it has no `src/`, so nothing from
    // a user project can leak into the stdlib denominator.
    let mods = collect_module_sources(repo_root, &stdlib_root).map_err(|(_, problems)| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            format!(
                "{} stdlib module(s) cannot be measured:\n  - {}",
                problems.len(),
                problems.join("\n  - ")
            ),
        )
    })?;

    // A `type` entry is one whose signature starts with `type ` — the same rule
    // any consumer of symbols.json applies (`type alias X` / `type X = A | B`);
    // a value annotation is a type EXPRESSION and can never start with `type `.
    let is_type = |s: &DocSym| s.signature.starts_with("type ");

    let mut out: Vec<ModuleDenominator> = Vec::new();
    let mut seen = std::collections::BTreeSet::new();
    for m in mods {
        if !m.path.starts_with(&stdlib_root) || !seen.insert(m.name.clone()) {
            continue;
        }
        let filtered = module_symbols_with(&m.src, true);
        let unfiltered = module_symbols_with(&m.src, false);
        let ftypes = filtered.iter().filter(|s| is_type(s)).count();
        let utypes = unfiltered.iter().filter(|s| is_type(s)).count();
        let exposes_all = {
            let parse = syntax::parse(&m.src, FileId(0));
            exposing_set(&m.src, &parse.tree()).is_none()
        };
        out.push(ModuleDenominator {
            module: m.name,
            exposes_all,
            filtered_entries: filtered.len(),
            filtered_values: filtered.len() - ftypes,
            filtered_types: ftypes,
            unfiltered_entries: unfiltered.len(),
            unfiltered_values: unfiltered.len() - utypes,
            unfiltered_types: utypes,
        });
    }
    out.sort_by(|a, b| a.module.cmp(&b.module));
    Ok(out)
}

fn render_pre_with_anchors(page: &str, syms: &[DocSym]) -> String {
    let names: std::collections::BTreeSet<&str> = syms.iter().map(|s| s.name.as_str()).collect();
    let mut out = String::new();
    for line in page.lines() {
        if let Some(rest) = line.strip_prefix("  ") {
            if let Some(id) = leading_ident(rest) {
                if names.contains(id.as_str()) {
                    out.push_str("  <span id=\"");
                    out.push_str(&html_escape(&id));
                    out.push_str("\">");
                    out.push_str(&html_escape(rest));
                    out.push_str("</span>\n");
                    continue;
                }
            }
        }
        out.push_str(&html_escape(line));
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
    let raw = exposing.syntax().text().to_string();
    // Peel a leading `exposing` keyword (if the node text includes it) + the
    // outer parens, leaving the comma-separated export items.
    let trimmed = raw.trim();
    let trimmed = trimmed.strip_prefix("exposing").map(str::trim).unwrap_or(trimmed);
    let inner = trimmed
        .strip_prefix('(')
        .and_then(|s| s.strip_suffix(')'))
        .unwrap_or(trimmed);
    // Split TOP-LEVEL items by comma. A whole-module export is a single `..`
    // item; a `Type(..)` constructor export keeps its `..` INSIDE nested parens,
    // so only a standalone top-level `..` means export-all. (The old code split
    // on every non-alphanumeric char, so `ColType(..)`'s inner `..` was misread
    // as export-all and the whole module's internals leaked into `sky doc`.)
    let mut names = std::collections::BTreeSet::new();
    for item in inner.split(',') {
        let item = item.trim();
        if item == ".." {
            return None; // whole-module export → no filtering
        }
        // Keep the leading identifier, dropping any `(..)` / `(Ctor, …)` group.
        let name = item.split('(').next().unwrap_or(item).trim();
        if !name.is_empty() && !name.contains('.') {
            names.insert(name.to_string());
        }
    }
    Some(names)
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

    /// The v0.19-migrated kernel modules render their full typed signatures +
    /// doc + example FROM the .sky source (single source of truth — no
    /// kernel_api entry). Reads the embedded stdlib .sky off disk and renders it
    /// the same way `render_module` does for a .sky-backed module.
    #[test]
    fn migrated_kernel_module_renders_full_sigs_from_sky_source() {
        let repo = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .ancestors()
            .nth(3)
            .expect("repo root")
            .to_path_buf();
        for module in ["Std/Tui", "Std/Live", "Std/Cli", "Std/Jobs"] {
            let path = repo.join("sky-stdlib").join(format!("{module}.sky"));
            let src = std::fs::read_to_string(&path)
                .unwrap_or_else(|e| panic!("read {}: {e}", path.display()));
            let page = render_source(&src);
            assert!(
                page.contains("-> Task Error ()") || page.contains("Task Error"),
                "{module}.sky must render typed Task signatures, got:\n{page}"
            );
            // No `?` / bare-name placeholders — real HM sigs only.
            assert!(!page.contains(" : ?"), "{module}.sky rendered a `?` sig");
        }
    }

    #[test]
    fn kernel_only_module_is_queryable() {
        // The legacy kernel-name render path returns None for a non-module.
        assert!(render_kernel_module("DefinitelyNotAModule").is_none());
    }

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
    fn union_variants_keep_their_argument_types() {
        // The doc renderer must show constructor args (`Resend String`), not just
        // the name — else the docs imply a nullary ctor the checker rejects.
        let src = "module M exposing (Provider(..))\n\n\
                   type Provider\n    = Resend String\n    | Ses Config\n    | Off\n";
        let page = render_source(src);
        assert!(
            page.contains("type Provider = Resend String | Ses Config | Off"),
            "union ctor args dropped:\n{page}"
        );
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
        // Serve mode: API index is index.html with root-absolute /m/ links (what
        // the bundled doc server serves). Export mode (reference.html + relative
        // links) is exercised by the doc-site export flow.
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
        // The search bar + its wiring are present.
        assert!(
            index.contains("type=\"search\""),
            "index has search input:\n{index}"
        );
        assert!(
            index.contains("/api/symbols.json"),
            "index fetches the manifest:\n{index}"
        );

        let list_page = std::fs::read_to_string(out.join("m").join("Sky.Core.List.html")).unwrap();
        assert!(
            list_page.contains("map : (a -&gt; b)"),
            "per-module page:\n{list_page}"
        );
        // The value line carries a per-symbol anchor for `#map` fragment links.
        assert!(
            list_page.contains("<span id=\"map\">map : (a -&gt; b)"),
            "per-symbol anchor:\n{list_page}"
        );

        let symbols = std::fs::read_to_string(out.join("api").join("symbols.json")).unwrap();
        // Unified shape read by BOTH the Go TUI loader + the serve search:
        // `{"entries":[{module,name,sig,bucket,summary}]}`.
        assert!(
            symbols.starts_with("{\"entries\":[") && symbols.ends_with("]}"),
            "symbols:\n{symbols}"
        );
        assert!(
            symbols.contains("\"module\":\"Sky.Core.List\""),
            "symbols:\n{symbols}"
        );
        assert!(
            symbols.contains("\"name\":\"map\""),
            "per-symbol name field:\n{symbols}"
        );
        assert!(
            symbols.contains("\"sig\":\"(a -> b) -> List a -> List b\""),
            "per-symbol sig field:\n{symbols}"
        );
        // `bucket` distinguishes stdlib vs project (the Go loader groups by it).
        assert!(
            symbols.contains("\"bucket\":\"stdlib\"") && symbols.contains("\"bucket\":\"project\""),
            "bucket field:\n{symbols}"
        );
        assert!(
            symbols.contains("\"name\":\"main\""),
            "project-module symbol:\n{symbols}"
        );

        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn module_symbols_lists_exported_bindings_in_order() {
        // Annotated value, unannotated exported value, a type, and an
        // unexposed helper (must be excluded).
        let src = "module M exposing (add, plain, Color)\n\n\
                   -- | sums two ints.\nadd : Int -> Int -> Int\nadd a b = a + b\n\n\
                   plain = 42\n\n\
                   type Color = Red | Green\n\n\
                   helper : Int\nhelper = 0\n";
        let syms = module_symbols(src);
        let names: Vec<&str> = syms.iter().map(|s| s.name.as_str()).collect();
        assert_eq!(names, vec!["add", "plain", "Color"], "order + export gate");

        let add = &syms[0];
        assert_eq!(add.signature, "Int -> Int -> Int");
        assert_eq!(add.summary, "sums two ints.");

        // Unannotated exported binding still appears, with empty signature.
        let plain = &syms[1];
        assert_eq!(plain.signature, "");

        let color = &syms[2];
        assert_eq!(color.signature, "type Color = Red | Green");
    }

    #[test]
    fn symbols_json_has_per_symbol_entries() {
        let root = std::env::temp_dir().join(format!("sky-syms-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let stdlib = root.join("sky-stdlib");
        std::fs::create_dir_all(&stdlib).unwrap();
        std::fs::write(
            stdlib.join("Str.sky"),
            "module Str exposing (len, cat)\n\nlen : String -> Int\nlen s = 0\n\ncat : String -> String -> String\ncat a b = a\n",
        )
        .unwrap();
        let proj = root.join("proj");
        std::fs::create_dir_all(proj.join("src")).unwrap();

        let out = root.join("out");
        render_doc_site(&root, &proj, &out).unwrap();
        let symbols = std::fs::read_to_string(out.join("api").join("symbols.json")).unwrap();

        // Two exported bindings → two entries, each with a "name" field (not
        // the old module-only shape).
        assert_eq!(
            symbols.matches("\"name\":").count(),
            2,
            "one entry per exported binding:\n{symbols}"
        );
        assert!(symbols.contains("\"name\":\"len\""), "symbols:\n{symbols}");
        assert!(symbols.contains("\"name\":\"cat\""), "symbols:\n{symbols}");
        // The old "module-only" manifest ({"module":"Str"} with no name) is gone.
        assert!(
            !symbols.contains("{\"module\":\"Str\"}"),
            "old module-only shape removed:\n{symbols}"
        );

        let _ = std::fs::remove_dir_all(&root);
    }
}

// ─── Prose guides (docs/*.md → static HTML) ────────────────────────────────
//
// The docs site's teaching layer: the live reference prose under `docs/`
// (EXCLUDING `docs/history/`) rendered to HTML with a shared nav, alongside the
// auto-generated API pages. Uses `pulldown-cmark` (tables/code/lists/etc.).
// Internal `*.md` links are resolved relative to the source doc and rewritten
// to the flattened guide filenames; links outside `docs/` (or into history) are
// left untouched so they resolve on GitHub.

const GUIDE_STYLE: &str = "<style>\
:root{color-scheme:light dark}\
body{font-family:system-ui,sans-serif;max-width:52rem;margin:0 auto;padding:0 1rem 3rem;line-height:1.6}\
nav.topnav{position:sticky;top:0;z-index:5;background:Canvas;border-bottom:1px solid #8884;padding:.7rem 0;margin:0 0 1.5rem;font-size:.95rem}\
nav.topnav a{color:#0b6;text-decoration:none;margin-right:.9rem}nav.topnav a:hover{text-decoration:underline}nav.topnav a[aria-current] b{color:inherit}\
h1{font-size:1.7rem}h2{font-size:1.3rem;margin-top:2rem;border-bottom:1px solid #8883;padding-bottom:.2rem}h3{font-size:1.1rem}\
a{color:#0b6}\
pre{background:#8881;padding:.8rem 1rem;border-radius:.5rem;overflow-x:auto}\
code{font-family:ui-monospace,monospace;font-size:.9em}\
:not(pre)>code{background:#8882;padding:.1rem .3rem;border-radius:.3rem}\
table{border-collapse:collapse;width:100%;margin:1rem 0;font-size:.92rem;display:block;overflow-x:auto}\
th,td{border:1px solid #8884;padding:.4rem .6rem;text-align:left}th{background:#8881}\
blockquote{border-left:3px solid #0b6;margin:1rem 0;padding:.2rem 0 .2rem 1rem;color:#666}\
img{max-width:100%}\
.toc-sec{margin:1.6rem 0}.toc-sec p.d{color:#666;margin:.1rem 0 .6rem}\
</style>";

/// Extra `<style>` for the landing page + the tour (sidebar/prev-next/cards).
/// Appended after `GUIDE_STYLE` on those pages.
const SITE_STYLE: &str = "<style>\
.hero{margin:2rem 0 1rem}.hero h1{font-size:2.3rem;line-height:1.15;margin:0 0 .4rem}\
.hero .tag{font-size:1.15rem;color:#666;margin:0 0 1.2rem;max-width:40rem}\
.doors{display:grid;grid-template-columns:repeat(auto-fit,minmax(15rem,1fr));gap:1rem;margin:1.5rem 0 2rem}\
.door{display:block;border:1px solid #8884;border-radius:.7rem;padding:1.1rem 1.2rem;text-decoration:none;color:inherit;background:#8881}\
.door:hover{border-color:#0b6;text-decoration:none}\
.door h3{margin:.1rem 0 .3rem;font-size:1.15rem;color:#0b6}.door p{margin:0;color:#666;font-size:.95rem}\
.pros{display:grid;grid-template-columns:repeat(auto-fit,minmax(14rem,1fr));gap:1rem 1.5rem;margin:1rem 0 2rem}\
.pros h3{font-size:1rem;margin:.2rem 0 .2rem}.pros p{margin:0;color:#666;font-size:.93rem}\
.cta{display:inline-block;background:#0b6;color:#fff;padding:.55rem 1.1rem;border-radius:.5rem;text-decoration:none;font-weight:600}\
.cta:hover{filter:brightness(1.08)}\
.tour{display:grid;grid-template-columns:16rem 1fr;gap:2rem;max-width:64rem}\
.tour aside{font-size:.9rem;border-right:1px solid #8884;padding-right:1rem}\
.tour aside a{color:inherit;text-decoration:none;display:block;padding:.25rem .4rem;border-radius:.35rem}\
.tour aside a:hover{background:#8881}\
.tour aside a.cur{background:#0b62;color:#0b6;font-weight:600}\
.tour aside a .n{color:#999}\
.tour aside .sec{color:#888;font-size:.78rem;text-transform:uppercase;letter-spacing:.04em;margin:1rem 0 .3rem}\
.tour aside .sec:first-child{margin-top:0}\
.tour article{min-width:0}\
.prevnext{display:flex;justify-content:space-between;gap:1rem;margin:2.5rem 0 0;padding-top:1rem;border-top:1px solid #8884;font-size:.95rem}\
.prevnext a{text-decoration:none;max-width:48%}.prevnext .nx{margin-left:auto;text-align:right}\
.prevnext .lbl{display:block;color:#999;font-size:.8rem}\
@media(max-width:46rem){.tour{grid-template-columns:1fr}.tour aside{border-right:0;border-bottom:1px solid #8884;padding:0 0 1rem}}\
</style>";

/// Flatten a docs-relative path (`skylive/overview.md`) to one guide filename
/// (`skylive-overview.html`). `README.md` at the docs root maps to `index`-safe
/// `readme.html` (the guide index is generated separately).
fn flatten_guide_name(rel_to_docs: &str) -> String {
    let stem = rel_to_docs.strip_suffix(".md").unwrap_or(rel_to_docs);
    format!("{}.html", stem.replace('/', "-").replace(' ', "-").to_lowercase())
}

/// Collect `*.md` under `dir` (recursively), EXCLUDING `docs/history/`, as
/// (path-relative-to-`docs_root`, absolute-path) pairs.
fn collect_guide_md(docs_root: &Path, dir: &Path, out: &mut Vec<(String, PathBuf)>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for e in entries.flatten() {
        let p = e.path();
        if p.is_dir() {
            if p.file_name().and_then(|s| s.to_str()) == Some("history") {
                continue;
            }
            collect_guide_md(docs_root, &p, out);
        } else if p.extension().and_then(|s| s.to_str()) == Some("md") {
            if let Ok(rel) = p.strip_prefix(docs_root) {
                out.push((rel.to_string_lossy().replace('\\', "/"), p));
            }
        }
    }
}

/// Resolve a relative `*.md` link (`../skydb/overview.md#x`) from a source doc's
/// docs-relative path into the flattened guide filename, or `None` if it points
/// outside `docs/` or into `history/` (left as-is for GitHub).
fn resolve_md_link(src_rel: &str, href: &str) -> Option<String> {
    let (path_part, frag) = match href.split_once('#') {
        Some((p, f)) => (p, Some(f)),
        None => (href, None),
    };
    if !path_part.ends_with(".md") {
        return None;
    }
    // Resolve `path_part` relative to the source doc's PARENT directory.
    let base = std::path::Path::new(src_rel).parent().unwrap_or(Path::new(""));
    let mut segs: Vec<&str> = base
        .to_str()
        .unwrap_or("")
        .split('/')
        .filter(|s| !s.is_empty())
        .collect();
    for part in path_part.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                segs.pop();
            }
            other => segs.push(other),
        }
    }
    let joined = segs.join("/");
    if joined.starts_with("history/") || joined.contains("/history/") {
        return None;
    }
    let mut out = flatten_guide_name(&joined);
    if let Some(f) = frag {
        out.push('#');
        out.push_str(f);
    }
    Some(out)
}

/// Render Markdown to an HTML body, rewriting each internal `href="…"` via the
/// supplied resolver (`None` → leave the link untouched).
fn render_md(md: &str, resolve: impl Fn(&str) -> Option<String>) -> String {
    use pulldown_cmark::{html, Options, Parser};
    let mut opts = Options::empty();
    opts.insert(Options::ENABLE_TABLES);
    opts.insert(Options::ENABLE_STRIKETHROUGH);
    opts.insert(Options::ENABLE_FOOTNOTES);
    let mut body = String::new();
    html::push_html(&mut body, Parser::new_ext(md, opts));
    let mut out = String::with_capacity(body.len());
    let mut rest = body.as_str();
    while let Some(i) = rest.find("href=\"") {
        out.push_str(&rest[..i + 6]);
        rest = &rest[i + 6..];
        if let Some(j) = rest.find('"') {
            let href = &rest[..j];
            match resolve(href) {
                Some(rewritten) => out.push_str(&rewritten),
                None => out.push_str(href),
            }
            out.push('"');
            rest = &rest[j + 1..];
        }
    }
    out.push_str(rest);
    out
}

/// Render one guide Markdown doc, rewriting internal `*.md` links to their
/// flattened guide filenames (relative to the source doc's directory).
fn markdown_to_html(md: &str, src_rel: &str) -> String {
    render_md(md, |href| resolve_md_link(src_rel, href))
}

/// Render one tour lesson (all lessons live flat under `docs/learn/`). A `*.md`
/// link to a sibling lesson becomes `<stem>.html`; a link out to another doc
/// becomes `../guide/<flattened>.html`.
fn markdown_to_html_learn(md: &str) -> String {
    render_md(md, resolve_learn_md_link)
}

/// Resolve a `*.md` link written inside a `docs/learn/` lesson. Sibling lessons
/// resolve to `<stem>.html`; anything else under `docs/` to
/// `../guide/<flattened>.html`; links outside `docs/` (or into history) are left
/// untouched.
fn resolve_learn_md_link(href: &str) -> Option<String> {
    let (path_part, frag) = match href.split_once('#') {
        Some((p, f)) => (p, Some(f)),
        None => (href, None),
    };
    if !path_part.ends_with(".md") {
        return None;
    }
    let mut segs: Vec<&str> = vec!["learn"];
    for part in path_part.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                segs.pop();
            }
            other => segs.push(other),
        }
    }
    let joined = segs.join("/");
    if joined.starts_with("history/") || joined.contains("/history/") {
        return None;
    }
    let mut out = if let Some(stem) = joined.strip_prefix("learn/") {
        flatten_guide_name(stem) // sibling lesson: `01-first-app.md` → `01-first-app.html`
    } else {
        format!("../guide/{}", flatten_guide_name(&joined))
    };
    if let Some(f) = frag {
        out.push('#');
        out.push_str(f);
    }
    Some(out)
}

fn guide_title(rel: &str, md: &str) -> String {
    // Prefer the first `# ` heading; else the filename stem.
    for line in md.lines() {
        if let Some(h) = line.strip_prefix("# ") {
            return h.trim().to_string();
        }
    }
    rel.rsplit('/')
        .next()
        .unwrap_or(rel)
        .strip_suffix(".md")
        .unwrap_or(rel)
        .replace('-', " ")
}

fn wrap_guide_page(title: &str, body: &str) -> String {
    format!(
        "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">\
         <meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\
         <title>{} — Sky</title>{}</head><body>{}\
         <article>{}</article></body></html>\n",
        html_escape(title),
        GUIDE_STYLE,
        topnav("../", "guides"),
        body,
    )
}

/// Curated site map for the prose docs (everything under `docs/`, minus
/// `history/`). Returns `Some(section)` for a doc that belongs on the site, or
/// `None` to EXCLUDE it (legacy Haskell architecture, version roadmaps, RFCs,
/// planning notes, findings — real but not user-facing live reference).
///
/// `rel` is the docs-relative path (`skylive/overview.md`). `learn/` is handled
/// by the tour, not here, so it is excluded too.
fn guide_section(rel: &str) -> Option<&'static str> {
    // Hard excludes — not live user-facing reference.
    let excluded_exact = [
        "README.md",                                  // replaced by the landing
        "architecture/sky-compiler-architecture.md",  // LEGACY Haskell pipeline
        "conformance-findings.md",
        "rust-rewrite/12-migration-and-milestones.md",
        "skywebview/PLAN.md",
    ];
    if excluded_exact.contains(&rel) {
        return None;
    }
    // Excluded directories: the tour owns learn/; the rest are plans/roadmaps.
    for dir in ["learn/", "v0.19/", "rfcs/", "testing/"] {
        if rel.starts_with(dir) {
            return None;
        }
    }
    // Contributor / compiler-internals section.
    for dir in ["rust-rewrite/", "architecture/", "ffi/", "errors/"] {
        if rel.starts_with(dir) {
            return Some("Compiler &amp; contributing");
        }
    }
    if rel == "development.md" {
        return Some("Compiler &amp; contributing");
    }
    // Everything else that survived is a user-facing guide.
    Some("Guides")
}

/// Render the live prose docs (`docs/`, excluding `docs/history/`) into
/// `<out_dir>/guide/<flattened>.html` + a grouped `guide/index.html` TOC.
/// Called by `sky doc --export` after the API pages so the site carries both
/// the auto-generated reference AND the teaching prose.
pub fn render_guides(repo_root: &Path, out_dir: &Path) -> std::io::Result<()> {
    let docs = repo_root.join("docs");
    if !docs.is_dir() {
        return Ok(());
    }
    let guide_dir = out_dir.join("guide");
    std::fs::create_dir_all(&guide_dir)?;

    let mut pages: Vec<(String, PathBuf)> = Vec::new();
    collect_guide_md(&docs, &docs, &mut pages);
    pages.sort_by(|a, b| a.0.cmp(&b.0));

    // Per-page render + a grouped TOC, curated by `guide_section` (excludes the
    // tour, legacy Haskell architecture, roadmaps, RFCs, planning notes).
    let mut groups: BTreeMap<&'static str, Vec<(String, String)>> = BTreeMap::new();
    for (rel, path) in &pages {
        let Some(section) = guide_section(rel) else {
            continue;
        };
        let src = std::fs::read_to_string(path).unwrap_or_default();
        let title = guide_title(rel, &src);
        let body = markdown_to_html(&src, rel);
        std::fs::write(guide_dir.join(flatten_guide_name(rel)), wrap_guide_page(&title, &body))?;
        groups
            .entry(section)
            .or_default()
            .push((title, flatten_guide_name(rel)));
    }

    // guide/index.html — sectioned TOC. "Guides" (user-facing deep dives) first,
    // then "Compiler & contributing". A short description sits under each header.
    let mut inner = String::from(
        "<h1>Guides &amp; internals</h1>\
         <p>Topic deep-dives and compiler internals. New to Sky? Take the \
         <a href=\"../learn/index.html\">tour</a> first. Looking up a module? See the \
         <a href=\"../reference.html\">API reference</a>.</p>",
    );
    let section_order = [
        (
            "Guides",
            "How to build things — the runtime, data, UI, auth, tooling, deployment.",
        ),
        (
            "Compiler &amp; contributing",
            "For contributors: the Rust compiler architecture, the FFI boundary, stdlib correctness, and how to work in the repo.",
        ),
    ];
    for (section, desc) in section_order {
        let Some(items) = groups.get_mut(section) else {
            continue;
        };
        items.sort_by(|a, b| a.0.cmp(&b.0));
        inner.push_str(&format!(
            "<div class=\"toc-sec\"><h2>{section}</h2><p class=\"d\">{desc}</p><ul>"
        ));
        for (title, file) in items.iter() {
            inner.push_str(&format!(
                "<li><a href=\"{}\">{}</a></li>",
                html_escape(file),
                html_escape(title)
            ));
        }
        inner.push_str("</ul></div>");
    }
    std::fs::write(guide_dir.join("index.html"), wrap_guide_page("Guides & internals", &inner))?;
    Ok(())
}

// ── The "Learn Sky" tour ──────────────────────────────────────────────────
// A Tour-of-Go-style progressive curriculum: an ordered set of lessons under
// `docs/learn/`, rendered with a persistent sidebar + prev/next. The order +
// grouping live in `LEARN_TOUR`; the prose lives in the `.md` files, so the
// content stays live-editable while the structure is one place.

/// One tour lesson: the `docs/learn/<stem>.md` source, its sidebar label, and
/// the sidebar group it sits under. The `index` stem renders to
/// `learn/index.html` (the tour's front door); every other stem to
/// `learn/<stem>.html`.
struct Lesson {
    stem: &'static str,
    title: &'static str,
    section: &'static str,
}

const LEARN_TOUR: &[Lesson] = &[
    Lesson { stem: "index", title: "Welcome", section: "Start" },
    Lesson { stem: "01-first-app", title: "Your first app", section: "Start" },
    Lesson { stem: "02-values-and-types", title: "Values & types", section: "The language" },
    Lesson { stem: "03-functions", title: "Functions", section: "The language" },
    Lesson { stem: "04-records", title: "Records", section: "The language" },
    Lesson { stem: "05-unions-and-case", title: "Unions & case", section: "The language" },
    Lesson { stem: "06-lists", title: "Lists", section: "The language" },
    Lesson { stem: "07-maybe-and-result", title: "Maybe & Result", section: "The language" },
    Lesson { stem: "08-pipelines-and-let", title: "Pipelines & let", section: "The language" },
    Lesson { stem: "09-effects-and-task", title: "Effects & Task", section: "The language" },
    Lesson { stem: "10-modules", title: "Modules & imports", section: "The language" },
    Lesson { stem: "11-first-web-app", title: "Your first web app", section: "Building apps" },
    Lesson { stem: "12-ui", title: "UI with Std.Ui", section: "Building apps" },
    Lesson { stem: "13-forms-and-events", title: "Forms & events", section: "Building apps" },
    Lesson { stem: "14-routing", title: "Routing & navigation", section: "Building apps" },
    Lesson { stem: "15-data", title: "Data with Std.Db", section: "Building apps" },
    Lesson { stem: "16-auth", title: "Auth", section: "Building apps" },
    Lesson { stem: "17-deploying", title: "Deploying", section: "Building apps" },
    Lesson {
        stem: "18-coming-from-other-languages",
        title: "Coming from another language",
        section: "Next steps",
    },
    Lesson { stem: "19-ai-tooling", title: "Using AI tools", section: "Next steps" },
];

fn lesson_out_file(stem: &str) -> String {
    if stem == "index" {
        "index.html".to_string()
    } else {
        format!("{stem}.html")
    }
}

/// The tour sidebar: every lesson grouped by section, numbered (the welcome page
/// is unnumbered), with the current lesson highlighted.
fn tour_sidebar(active: usize) -> String {
    let mut out = String::from("<aside><nav>");
    let mut cur_section = "";
    let mut num = 0u32;
    for (i, l) in LEARN_TOUR.iter().enumerate() {
        if l.section != cur_section {
            out.push_str(&format!("<div class=\"sec\">{}</div>", html_escape(l.section)));
            cur_section = l.section;
        }
        let label = if l.stem == "index" {
            html_escape(l.title)
        } else {
            num += 1;
            format!("<span class=\"n\">{num}</span> {}", html_escape(l.title))
        };
        let cls = if i == active { " class=\"cur\"" } else { "" };
        out.push_str(&format!(
            "<a href=\"{}\"{cls}>{label}</a>",
            lesson_out_file(l.stem)
        ));
    }
    out.push_str("</nav></aside>");
    out
}

fn tour_prevnext(i: usize) -> String {
    let mut out = String::from("<div class=\"prevnext\">");
    if i > 0 {
        let p = &LEARN_TOUR[i - 1];
        out.push_str(&format!(
            "<a class=\"pv\" href=\"{}\"><span class=\"lbl\">&larr; Previous</span>{}</a>",
            lesson_out_file(p.stem),
            html_escape(p.title)
        ));
    }
    if i + 1 < LEARN_TOUR.len() {
        let n = &LEARN_TOUR[i + 1];
        out.push_str(&format!(
            "<a class=\"nx\" href=\"{}\"><span class=\"lbl\">Next &rarr;</span>{}</a>",
            lesson_out_file(n.stem),
            html_escape(n.title)
        ));
    }
    out.push_str("</div>");
    out
}

fn wrap_tour_page(title: &str, sidebar: &str, body: &str, prevnext: &str) -> String {
    format!(
        "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">\
         <meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\
         <title>{} — Learn Sky</title>{}{}</head><body>{}\
         <div class=\"tour\">{}<article>{}{}</article></div></body></html>\n",
        html_escape(title),
        GUIDE_STYLE,
        SITE_STYLE,
        topnav("../", "learn"),
        sidebar,
        body,
        prevnext,
    )
}

/// Render the `docs/learn/` tour into `<out_dir>/learn/`. A lesson listed in
/// `LEARN_TOUR` whose `.md` is missing renders a small placeholder (so the tour
/// never has a dead sidebar link while content is being authored).
pub fn render_learn_tour(repo_root: &Path, out_dir: &Path) -> std::io::Result<()> {
    let learn_src = repo_root.join("docs").join("learn");
    let learn_out = out_dir.join("learn");
    std::fs::create_dir_all(&learn_out)?;
    for (i, l) in LEARN_TOUR.iter().enumerate() {
        let src_path = learn_src.join(format!("{}.md", l.stem));
        let md = std::fs::read_to_string(&src_path).unwrap_or_else(|_| {
            format!("# {}\n\n_This lesson is being written._\n", l.title)
        });
        let body = markdown_to_html_learn(&md);
        let page = wrap_tour_page(l.title, &tour_sidebar(i), &body, &tour_prevnext(i));
        std::fs::write(learn_out.join(lesson_out_file(l.stem)), page)?;
    }
    Ok(())
}

// ── The landing page ──────────────────────────────────────────────────────

/// Render the site root `index.html`: what Sky is, why, and the three doors
/// (Learn / API reference / Guides & internals). Hand-written — it is the one
/// page that is marketing rather than generated reference.
pub fn render_landing(out_dir: &Path) -> std::io::Result<()> {
    let mut b = String::new();
    b.push_str("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">");
    b.push_str("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">");
    b.push_str("<title>Sky — one language for the whole stack</title>");
    b.push_str(GUIDE_STYLE);
    b.push_str(SITE_STYLE);
    b.push_str("</head><body>");
    b.push_str(&topnav("", "home"));
    b.push_str(
        "<section class=\"hero\">\
         <h1>Sky</h1>\
         <p class=\"tag\">A pure-functional, Elm-family language that compiles to typed Go. \
         One language for the whole stack — web, API, CLI, terminal, desktop — with a single \
         promise: <b>if it compiles, it works.</b></p>\
         <p><a class=\"cta\" href=\"learn/index.html\">Start the tour →</a></p>\
         </section>",
    );
    b.push_str("<div class=\"doors\">");
    for (href, title, desc) in [
        (
            "learn/index.html",
            "Learn Sky",
            "New here? A guided tour from your first app to a real web app — plus a chapter for developers coming from JavaScript, Python, Go, or Rust.",
        ),
        (
            "reference.html",
            "API reference",
            "Every standard-library module, generated from source and searchable. The place to look up a function or type.",
        ),
        (
            "guide/index.html",
            "Guides & internals",
            "Topic deep-dives (Sky.Live, Std.Db, Std.Ui, auth, deployment) and — for contributors — the Rust compiler architecture.",
        ),
    ] {
        b.push_str(&format!(
            "<a class=\"door\" href=\"{href}\"><h3>{title}</h3><p>{desc}</p></a>"
        ));
    }
    b.push_str("</div>");
    b.push_str("<h2>Why Sky</h2><div class=\"pros\">");
    for (h, p) in [
        (
            "One language, whole stack",
            "The same view code renders on the web (Sky.Live), the terminal (Sky.Tui), and the desktop (Sky.Webview). No separate front-end language, no serialization glue.",
        ),
        (
            "Errors are values, effects are explicit",
            "Fallible things return Result Error a; side effects return Task Error a. The type tells you what can go wrong and what touches the outside world. No null, no hidden throws.",
        ),
        (
            "Batteries included",
            "Auth, DB (one codec drives JSON and the database), UI, HTTP, money/decimals, jobs, observability — all in the standard library, all reviewed for security and scale.",
        ),
        (
            "It compiles to Go",
            "You get Go's deployment story — a single static binary — and its ecosystem (any Go package via FFI, no hand-written bindings).",
        ),
    ] {
        b.push_str(&format!("<div><h3>{h}</h3><p>{p}</p></div>"));
    }
    b.push_str("</div>");
    b.push_str(
        "<h2>Hello, Sky</h2>\
         <pre><code>module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         type Msg = Increment | Decrement\n\n\
         update : Msg -&gt; Int -&gt; Int\n\
         update msg count =\n\
         &nbsp;&nbsp;&nbsp;&nbsp;case msg of\n\
         &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Increment -&gt; count + 1\n\
         &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Decrement -&gt; count - 1\n\n\
         main =\n\
         &nbsp;&nbsp;&nbsp;&nbsp;println (String.fromInt (update Increment 0))</code></pre>\
         <p style=\"color:#666\">Ready? <a href=\"learn/index.html\">Take the tour →</a></p>",
    );
    b.push_str("</body></html>\n");
    std::fs::write(out_dir.join("index.html"), b)?;
    Ok(())
}
