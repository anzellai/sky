//! Std.I18n at build time: the `[i18n]` section of `sky.toml`, the catalogue
//! check, the embedded catalogue and the Go build tags.
//!
//! One parser. The message syntax is read by the Go runtime
//! (`runtime-go/rt/i18n`), and a tenant override is a message that arrives at
//! run time, so the runtime must parse messages. To keep the build and the
//! program from ever reading a message differently, the build does not parse
//! messages itself: it compiles the runtime's own checker
//! (`rt/i18n/skyi18ncheck`) in the generated Go project and runs it. The
//! checker validates every catalogue, gives the Go build tags of the CLDR
//! data, and writes the catalogue the program embeds.
//!
//! Build tags. `sky_i18n` compiles the module's kernels into the runtime: the
//! build sets it when the program calls a Std.I18n kernel or the project has
//! an `[i18n]` section. `sky_cldr_<tag>` compiles one locale's CLDR data: the
//! checker names one per listed locale and CLDR parent. A program that does
//! not use the module gets no tag and links none of it.

use std::io::Write as _;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::Duration;

/// The generated Go file that installs the catalogue (package main).
pub const CATALOGUE_GO_FILE: &str = "i18n_catalogue_gen.go";
/// The build tags of the generated project, one per line, read by every
/// `go build` of it (native and wasm).
pub const TAGS_FILE: &str = "sky-build-tags.txt";
/// The checker binary, built in the generated project.
const CHECKER_BIN: &str = ".sky-i18n-check";
/// The keys of `[i18n]`.
pub const KEYS: &[&str] = &["locales", "default", "catalogues"];
/// The document that explains every E5xxx code.
pub const ERRORS_DOC: &str = "docs/errors/i18n-catalogues.md";

/// The `[i18n]` section.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct I18nConfig {
    pub locales: Vec<String>,
    pub default: String,
    pub catalogues: String,
}

/// One `key = value` of a section, with its value's raw text (a multi-line
/// array joined) and the 1-based line of the key.
struct Entry {
    key: String,
    raw: String,
    line: usize,
}

/// The entries of one section, or `None` when the section is absent.
fn section_entries(text: &str, section: &str) -> Option<Vec<Entry>> {
    let mut found = false;
    let mut cur = String::new();
    let mut out = Vec::new();
    let lines: Vec<&str> = text.lines().collect();
    let mut i = 0;
    while i < lines.len() {
        let line = strip_comment(lines[i]).trim().to_string();
        i += 1;
        if line.is_empty() {
            continue;
        }
        if line.starts_with('[') && !line.contains('=') {
            if let Some(end) = line.find(']') {
                cur = line[1..end].trim().trim_matches('"').to_string();
                found |= cur == section;
                continue;
            }
        }
        if cur != section {
            continue;
        }
        let Some((k, v)) = line.split_once('=') else {
            out.push(Entry {
                key: line.clone(),
                raw: String::new(),
                line: i,
            });
            continue;
        };
        let start_line = i;
        let mut raw = v.trim().to_string();
        // A multi-line array: join lines until the brackets balance.
        while raw.starts_with('[') && !brackets_balanced(&raw) && i < lines.len() {
            raw.push(' ');
            raw.push_str(strip_comment(lines[i]).trim());
            i += 1;
        }
        out.push(Entry {
            key: k.trim().to_string(),
            raw,
            line: start_line,
        });
    }
    found.then_some(out)
}

fn brackets_balanced(s: &str) -> bool {
    let mut depth = 0i32;
    let mut in_str = false;
    for c in s.chars() {
        match c {
            '"' => in_str = !in_str,
            '[' if !in_str => depth += 1,
            ']' if !in_str => depth -= 1,
            _ => {}
        }
    }
    depth <= 0
}

/// A line without its `#` comment (a `#` inside a string stays).
fn strip_comment(line: &str) -> &str {
    let mut in_str = false;
    for (i, c) in line.char_indices() {
        match c {
            '"' => in_str = !in_str,
            '#' if !in_str => return &line[..i],
            _ => {}
        }
    }
    line
}

/// The raw value of `section.key` in a project's `sky.toml`, a multi-line
/// array joined onto one line. The config-surface census counts each call of
/// this reader as a pre-binary read (`xtask config-surface`).
pub fn i18n_toml_value(project_dir: &Path, section: &str, key: &str) -> Option<String> {
    let text = std::fs::read_to_string(project_dir.join("sky.toml")).ok()?;
    section_entries(&text, section)?
        .into_iter()
        .find(|e| e.key == key)
        .map(|e| e.raw)
}

/// Whether the project's `sky.toml` has an `[i18n]` section.
pub fn has_section(project_dir: &Path) -> bool {
    std::fs::read_to_string(project_dir.join("sky.toml"))
        .ok()
        .and_then(|t| section_entries(&t, "i18n"))
        .is_some()
}

/// A TOML string: `"en-GB"`.
fn parse_string(raw: &str) -> Option<String> {
    let s = raw.trim();
    let inner = s.strip_prefix('"')?.strip_suffix('"')?;
    if inner.contains('"') || inner.contains('\\') {
        return None;
    }
    Some(inner.to_string())
}

/// A TOML array of strings: `["en-GB", "zh-Hans"]`.
fn parse_string_array(raw: &str) -> Option<Vec<String>> {
    let s = raw.trim();
    let inner = s.strip_prefix('[')?.strip_suffix(']')?;
    let mut out = Vec::new();
    for part in inner.split(',') {
        let p = part.trim();
        if p.is_empty() {
            continue; // a trailing comma
        }
        out.push(parse_string(p)?);
    }
    Some(out)
}

fn render(code: &str, loc: &str, message: &str) -> String {
    diagnostics::render_located(
        code,
        Some(loc),
        message,
        Some(&format!("{ERRORS_DOC}#{}", code.to_ascii_lowercase())),
    )
}

/// Read `[i18n]`. `Ok(None)` when the section is absent. `Err` is a rendered
/// E5001 diagnostic: an unknown key, a value of the wrong shape, a missing
/// key.
pub fn read_config(project_dir: &Path) -> Result<Option<I18nConfig>, String> {
    let Ok(text) = std::fs::read_to_string(project_dir.join("sky.toml")) else {
        return Ok(None);
    };
    let Some(entries) = section_entries(&text, "i18n") else {
        return Ok(None);
    };
    let mut errors = Vec::new();
    for e in &entries {
        if !KEYS.contains(&e.key.as_str()) {
            errors.push(render(
                "E5001",
                &format!("sky.toml:{}", e.line),
                &format!(
                    "[i18n] has no key `{}`. Its keys are locales, default and catalogues.",
                    e.key
                ),
            ));
        }
    }
    let locales_raw = i18n_toml_value(project_dir, "i18n", "locales");
    let default_raw = i18n_toml_value(project_dir, "i18n", "default");
    let catalogues_raw = i18n_toml_value(project_dir, "i18n", "catalogues");
    let line_of = |k: &str| {
        entries
            .iter()
            .find(|e| e.key == k)
            .map(|e| format!("sky.toml:{}", e.line))
            .unwrap_or_else(|| "sky.toml".to_string())
    };
    let locales = match locales_raw.as_deref().map(parse_string_array) {
        Some(Some(v)) => v,
        Some(None) => {
            errors.push(render(
                "E5001",
                &line_of("locales"),
                "[i18n] locales must be an array of strings, for example locales = [\"en-GB\", \"zh-Hans\"]",
            ));
            Vec::new()
        }
        None => {
            errors.push(render(
                "E5001",
                "sky.toml",
                "[i18n] needs locales, for example locales = [\"en-GB\", \"zh-Hans\"]",
            ));
            Vec::new()
        }
    };
    let default = match default_raw.as_deref().map(parse_string) {
        Some(Some(v)) => v,
        Some(None) => {
            errors.push(render(
                "E5001",
                &line_of("default"),
                "[i18n] default must be a string, for example default = \"en-GB\"",
            ));
            String::new()
        }
        None => String::new(), // the checker names the fix (E5001)
    };
    let catalogues = match catalogues_raw.as_deref().map(parse_string) {
        Some(Some(v)) => v,
        Some(None) => {
            errors.push(render(
                "E5001",
                &line_of("catalogues"),
                "[i18n] catalogues must be a string, the catalogue folder, for example catalogues = \"i18n\"",
            ));
            String::new()
        }
        None => "i18n".to_string(),
    };
    if !errors.is_empty() {
        return Err(errors.join("\n"));
    }
    Ok(Some(I18nConfig {
        locales,
        default,
        catalogues,
    }))
}

/// Whether emitted Go calls a Std.I18n kernel. The lowering keeps only the
/// code a program reaches, so a kernel name in `main.go` means the program
/// uses the module.
pub fn uses_i18n(main_go: &str) -> bool {
    main_go.contains("\"I18n_")
}

/// What `prepare` found, for the build's report.
#[derive(Debug, Default)]
pub struct Prepared {
    /// Coverage lines for locales that lack some of the default locale's keys.
    pub notes: Vec<String>,
}

/// Check the catalogues and write the generated files of the project in
/// `out_dir` (which holds `main.go` and the materialised runtime). `Err` is
/// the rendered diagnostics: the build fails.
pub fn prepare(project_dir: &Path, out_dir: &Path) -> Result<Prepared, String> {
    let cfg = read_config(project_dir)?;
    let main_go = std::fs::read_to_string(out_dir.join("main.go")).unwrap_or_default();
    let catalogue_file = out_dir.join(CATALOGUE_GO_FILE);
    let tags_file = out_dir.join(TAGS_FILE);
    let Some(cfg) = cfg else {
        let _ = std::fs::remove_file(&catalogue_file);
        if uses_i18n(&main_go) {
            write_if_changed(&tags_file, "sky_i18n\n")?;
        } else {
            let _ = std::fs::remove_file(&tags_file);
        }
        return Ok(Prepared::default());
    };
    let result = run_checker(project_dir, out_dir, &cfg)?;
    let diags = result
        .get("diagnostics")
        .and_then(|d| d.as_array())
        .cloned()
        .unwrap_or_default();
    if !diags.is_empty() {
        let rendered: Vec<String> = diags
            .iter()
            .map(|d| {
                let s = |k: &str| d.get(k).and_then(|v| v.as_str()).unwrap_or("");
                render(s("code"), s("file"), s("message"))
            })
            .collect();
        let _ = std::fs::remove_file(&catalogue_file);
        return Err(rendered.join("\n"));
    }
    let catalogue = result
        .get("catalogue")
        .and_then(|c| c.as_str())
        .ok_or("the i18n checker gave no catalogue")?;
    let tags: Vec<String> = result
        .get("tags")
        .and_then(|t| t.as_array())
        .map(|a| {
            a.iter()
                .filter_map(|t| t.as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default();
    let go = format!(
        "// Code generated by sky from the [i18n] catalogues (CLDR {}). DO NOT EDIT.\n\n\
         package main\n\n\
         import skyi18n \"sky-app/rt/i18n\"\n\n\
         func init() {{\n\
         \tif err := skyi18n.Install({}); err != nil {{\n\
         \t\tprintln(\"sky: \" + err.Error())\n\
         \t}}\n\
         }}\n",
        result.get("cldr").and_then(|v| v.as_str()).unwrap_or("?"),
        base::go_string_literal(catalogue)
    );
    write_if_changed(&catalogue_file, &go)?;
    write_if_changed(&tags_file, &(tags.join("\n") + "\n"))?;
    let mut prepared = Prepared::default();
    for c in result
        .get("coverage")
        .and_then(|c| c.as_array())
        .cloned()
        .unwrap_or_default()
    {
        let loc = c.get("locale").and_then(|v| v.as_str()).unwrap_or("");
        let keys = c.get("keys").and_then(|v| v.as_u64()).unwrap_or(0);
        let of = c.get("of").and_then(|v| v.as_u64()).unwrap_or(0);
        if keys < of {
            prepared.notes.push(format!(
                "i18n: {loc} has {keys} of {of} keys of the default locale {}; the other {} fall back to it",
                cfg.default,
                of - keys
            ));
        }
    }
    Ok(prepared)
}

fn write_if_changed(path: &Path, text: &str) -> Result<(), String> {
    if std::fs::read_to_string(path).ok().as_deref() == Some(text) {
        return Ok(());
    }
    std::fs::write(path, text).map_err(|e| format!("write {}: {e}", path.display()))
}

/// Build the runtime's checker in the generated project and run it.
fn run_checker(
    project_dir: &Path,
    out_dir: &Path,
    cfg: &I18nConfig,
) -> Result<serde_json::Value, String> {
    let bin: PathBuf = out_dir.join(CHECKER_BIN);
    let cmd = crate::go_cmd::GoBuild {
        dir: out_dir,
        target: crate::go_cmd::Target::Native { cgo: false },
        jobs: None,
        output: Some(bin.as_os_str()),
        packages: &["./rt/i18n/skyi18ncheck"],
        extra: &[],
    }
    .command();
    {
        let _cache = crate::go_cache::hold_shared();
        let limit = crate::build::go_build_timeout()?;
        let out = crate::build::run_bounded(cmd, limit)
            .map_err(|e| format!("failed to build the i18n catalogue checker: {e}"))?;
        if out.timed_out || !out.status.is_some_and(|s| s.success()) {
            return Err(format!(
                "the i18n catalogue checker did not build:\n{}",
                out.stderr.trim_end()
            ));
        }
    }
    let root = std::fs::canonicalize(project_dir).unwrap_or_else(|_| project_dir.to_path_buf());
    let input = serde_json::json!({
        "root": root.to_string_lossy(),
        "catalogues": cfg.catalogues,
        "locales": cfg.locales,
        "default": cfg.default,
    })
    .to_string();
    let mut child = Command::new(&bin)
        .current_dir(project_dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("failed to run the i18n catalogue checker: {e}"))?;
    if let Some(mut stdin) = child.stdin.take() {
        stdin
            .write_all(input.as_bytes())
            .map_err(|e| format!("i18n checker stdin: {e}"))?;
    }
    let (tx, rx) = std::sync::mpsc::channel();
    let pid = child.id();
    std::thread::spawn(move || {
        let _ = tx.send(child.wait_with_output());
    });
    let out = match rx.recv_timeout(Duration::from_secs(300)) {
        Ok(r) => r.map_err(|e| format!("i18n catalogue checker: {e}"))?,
        Err(_) => {
            #[cfg(unix)]
            {
                let _ = Command::new("kill").arg("-9").arg(pid.to_string()).status();
            }
            let _ = pid;
            return Err(
                "the i18n catalogue checker ran for more than 300 s and was stopped".into(),
            );
        }
    };
    if !out.status.success() {
        return Err(format!(
            "the i18n catalogue checker failed:\n{}",
            String::from_utf8_lossy(&out.stderr).trim_end()
        ));
    }
    serde_json::from_slice(&out.stdout)
        .map_err(|e| format!("the i18n catalogue checker gave unreadable output: {e}"))
}

/// The `-tags=` argument for `go build` of a generated project, or `None`.
/// A `-tags` on the command line replaces one in `GOFLAGS`, so the user's own
/// `GOFLAGS` tags are kept in it.
pub fn tags_arg(out_dir: &Path) -> Option<String> {
    let text = std::fs::read_to_string(out_dir.join(TAGS_FILE)).ok()?;
    let mut tags: Vec<String> = text
        .lines()
        .map(str::trim)
        .filter(|l| !l.is_empty())
        .map(str::to_string)
        .collect();
    if tags.is_empty() {
        return None;
    }
    let goflags = std::env::var("GOFLAGS").unwrap_or_default();
    for f in goflags.split_whitespace() {
        if let Some(v) = f
            .strip_prefix("-tags=")
            .or_else(|| f.strip_prefix("--tags="))
        {
            tags.extend(v.split(',').filter(|t| !t.is_empty()).map(str::to_string));
        }
    }
    tags.sort();
    tags.dedup();
    Some(format!("-tags={}", tags.join(",")))
}

/// The `[i18n]` section for a generated Sky.Spa half, with the catalogue
/// folder made absolute (the half's own folder is `.split/<half>`). Empty when
/// the project has no `[i18n]`.
pub fn spa_section(project_dir: &Path) -> String {
    let Ok(Some(cfg)) = read_config(project_dir) else {
        // An absent or bad section: a bad one is reported by the half's own
        // build, from the copied lines.
        return match std::fs::read_to_string(project_dir.join("sky.toml"))
            .ok()
            .and_then(|t| section_entries(&t, "i18n"))
        {
            Some(entries) => {
                let mut s = String::from("\n[i18n]\n");
                for e in entries {
                    s.push_str(&format!("{} = {}\n", e.key, e.raw));
                }
                s
            }
            None => String::new(),
        };
    };
    let folder = Path::new(&cfg.catalogues);
    let abs = if folder.is_absolute() {
        folder.to_path_buf()
    } else {
        std::fs::canonicalize(project_dir)
            .unwrap_or_else(|_| project_dir.to_path_buf())
            .join(folder)
    };
    let quote = |s: &str| format!("\"{s}\"");
    format!(
        "\n[i18n]\nlocales = [{}]\ndefault = {}\ncatalogues = {}\n",
        cfg.locales
            .iter()
            .map(|l| quote(l))
            .collect::<Vec<_>>()
            .join(", "),
        quote(&cfg.default),
        quote(&abs.to_string_lossy().replace('\\', "/"))
    )
}

/// A copy of `sky.toml` for a staged project (a Std.App leg under
/// `.skyapp/`): the `[i18n]` section is replaced by [`spa_section`], whose
/// catalogue folder is absolute, so the staged project reads the project's
/// own catalogues. Text with no `[i18n]` section is returned unchanged.
pub fn absolutize_manifest(text: &str, project_dir: &Path) -> String {
    if section_entries(text, "i18n").is_none() {
        return text.to_string();
    }
    let mut out = String::new();
    let mut in_i18n = false;
    for line in text.lines() {
        let t = strip_comment(line).trim();
        if t.starts_with('[') && !t.contains('=') {
            in_i18n = t.trim_start_matches('[').trim_end_matches(']').trim() == "i18n";
        }
        if !in_i18n {
            out.push_str(line);
            out.push('\n');
        }
    }
    out.push_str(&spa_section(project_dir));
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A scratch folder, removed on drop.
    struct Scratch(PathBuf);
    impl Scratch {
        fn path(&self) -> &Path {
            &self.0
        }
    }
    impl Drop for Scratch {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }
    fn scratch() -> Scratch {
        use std::sync::atomic::{AtomicU64, Ordering};
        static N: AtomicU64 = AtomicU64::new(0);
        let p = std::env::temp_dir().join(format!(
            "sky-i18n-test-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::SeqCst)
        ));
        std::fs::create_dir_all(&p).unwrap();
        Scratch(p)
    }

    fn project(toml: &str) -> Scratch {
        let d = scratch();
        std::fs::write(d.path().join("sky.toml"), toml).unwrap();
        d
    }

    #[test]
    fn reads_a_one_line_and_a_multi_line_section() {
        let d = project(
            "name = \"x\"\n[i18n]\nlocales = [\"en-GB\", \"zh-Hans\"] # the app's\ndefault = \"en-GB\"\n",
        );
        let c = read_config(d.path()).unwrap().unwrap();
        assert_eq!(c.locales, vec!["en-GB", "zh-Hans"]);
        assert_eq!(c.default, "en-GB");
        assert_eq!(c.catalogues, "i18n");
        let d = project(
            "[i18n]\nlocales = [\n  \"en-GB\",\n  \"ar\", # Arabic\n]\ndefault = \"en-GB\"\ncatalogues = \"lang\"\n[log]\nlevel = \"info\"\n",
        );
        let c = read_config(d.path()).unwrap().unwrap();
        assert_eq!(c.locales, vec!["en-GB", "ar"]);
        assert_eq!(c.catalogues, "lang");
    }

    #[test]
    fn no_section_is_none() {
        let d = project("name = \"x\"\n[log]\nlevel = \"info\"\n");
        assert_eq!(read_config(d.path()).unwrap(), None);
        assert!(!has_section(d.path()));
    }

    #[test]
    fn an_unknown_key_is_e5001() {
        let d = project("[i18n]\nlocales = [\"en\"]\ndefault = \"en\"\nlocale = \"en\"\n");
        let e = read_config(d.path()).unwrap_err();
        assert!(e.contains("[E5001]"), "{e}");
        assert!(e.contains("sky.toml:4"), "{e}");
        assert!(e.contains("no key `locale`"), "{e}");
    }

    #[test]
    fn a_bad_shape_is_e5001() {
        let d = project("[i18n]\nlocales = \"en\"\ndefault = \"en\"\n");
        let e = read_config(d.path()).unwrap_err();
        assert!(
            e.contains("[E5001]") && e.contains("array of strings"),
            "{e}"
        );
        let d = project("[i18n]\ndefault = \"en\"\n");
        let e = read_config(d.path()).unwrap_err();
        assert!(e.contains("needs locales"), "{e}");
    }

    #[test]
    fn tags_arg_keeps_the_users_goflags_tags() {
        let d = scratch();
        assert_eq!(tags_arg(d.path()), None);
        std::fs::write(d.path().join(TAGS_FILE), "sky_i18n\nsky_cldr_zh\n").unwrap();
        let t = tags_arg(d.path()).unwrap();
        assert!(t.starts_with("-tags=") && t.contains("sky_i18n") && t.contains("sky_cldr_zh"));
    }

    #[test]
    fn uses_i18n_reads_kernel_names() {
        assert!(uses_i18n("x := rt.Ffi_callPure(\"I18n_render\", args)"));
        assert!(!uses_i18n("package main"));
    }

    #[test]
    fn spa_section_makes_the_folder_absolute() {
        let d = project("[i18n]\nlocales = [\"en\"]\ndefault = \"en\"\n");
        let s = spa_section(d.path());
        assert!(s.contains("locales = [\"en\"]"), "{s}");
        let abs = std::fs::canonicalize(d.path()).unwrap().join("i18n");
        assert!(s.contains(&abs.to_string_lossy().to_string()), "{s}");
    }
}
