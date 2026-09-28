//! Local path dependencies — `sky add ./path`.
//!
//! A path dependency is a directory on this machine, recorded in `sky.toml` as
//! an inline table with a `path` key, in the section its kind belongs to:
//!
//! ```toml
//! ["go.dependencies"]
//! "example.com/greet" = { path = "../greet" }   # a Go module (has go.mod)
//!
//! [dependencies]
//! "widgets" = { path = "../widgets" }            # a Sky package (sky.toml / src/*.sky)
//! ```
//!
//! A relative `path` is relative to the PROJECT ROOT (the directory holding
//! `sky.toml`), never to the working directory, and is resolved against it on
//! every use. An absolute `path` is kept as written.
//!
//! Nothing is copied. A Go module is wired into the generated `go.mod` with a
//! `require <module> v0.0.0` plus a `replace <module> => <absolute dir>`, and the
//! build re-applies both on EVERY build, because the build rewrites `go.mod`
//! from the runtime's copy each time. A Sky package's modules load straight from
//! the directory's source root, beside `.skydeps/`.

use crate::ffi_ops::section_matches;
use std::path::{Component, Path, PathBuf};
use std::process::Command;

/// Which `sky.toml` section a path dependency lives in.
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum PathDepKind {
    /// `["go.dependencies"]` — a Go module; the key is its module path.
    Go,
    /// `[dependencies]` — a Sky package; the key is its name.
    Sky,
}

impl PathDepKind {
    pub fn section(self) -> &'static str {
        match self {
            PathDepKind::Go => "go.dependencies",
            PathDepKind::Sky => "dependencies",
        }
    }
}

/// One declared path dependency.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct PathDep {
    /// The Go module path (Go) or the package name (Sky).
    pub key: String,
    /// The `path` value exactly as `sky.toml` records it.
    pub path: String,
    pub kind: PathDepKind,
}

impl PathDep {
    /// The dependency's directory: `path` joined onto the project root when it
    /// is relative, then normalised lexically (`a/../b` → `b`). It does not
    /// touch the file system, so a missing directory still has an answer.
    pub fn resolve(&self, project_dir: &Path) -> PathBuf {
        let p = Path::new(&self.path);
        let joined = if p.is_absolute() {
            p.to_path_buf()
        } else {
            project_dir.join(p)
        };
        normalise(&joined)
    }
}

/// Lexical normalisation: drop `.` and fold `..` into its parent.
pub(crate) fn normalise(p: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for c in p.components() {
        match c {
            Component::CurDir => {}
            Component::ParentDir => {
                if !out.pop() {
                    out.push("..");
                }
            }
            other => out.push(other.as_os_str()),
        }
    }
    out
}

/// Whether a `sky add` / `sky remove` argument names a directory rather than an
/// import path: `.`, `..`, or anything starting `./`, `../` or `/` (and the
/// Windows spellings).
pub fn is_path_arg(arg: &str) -> bool {
    arg == "."
        || arg == ".."
        || arg.starts_with("./")
        || arg.starts_with("../")
        || arg.starts_with(".\\")
        || arg.starts_with("..\\")
        || Path::new(arg).is_absolute()
}

/// The `path` inside an inline-table dependency value, `{ path = "../x" }`.
/// `None` for a plain version string or an inline table with no `path`.
pub fn inline_path(value: &str) -> Option<String> {
    let v = value.trim();
    let inner = v.strip_prefix('{')?.strip_suffix('}')?;
    for part in inner.split(',') {
        let Some((k, val)) = part.split_once('=') else {
            continue;
        };
        if k.trim().trim_matches('"') == "path" {
            let val = val.trim();
            let unq = val
                .strip_prefix('"')
                .and_then(|s| s.strip_suffix('"'))
                .or_else(|| val.strip_prefix('\'').and_then(|s| s.strip_suffix('\'')))?;
            return Some(unq.to_string());
        }
    }
    None
}

/// Every path dependency declared in `sky.toml`, Go first then Sky, each in
/// declaration order. Absent / unreadable file → empty.
pub fn read_path_dependencies(sky_toml: &Path) -> Vec<PathDep> {
    let Ok(text) = std::fs::read_to_string(sky_toml) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for kind in [PathDepKind::Go, PathDepKind::Sky] {
        let mut in_section = false;
        for raw in text.lines() {
            let line = raw.trim();
            if line.starts_with('[') && line.ends_with(']') {
                in_section = section_matches(line, kind.section());
                continue;
            }
            if !in_section || line.is_empty() || line.starts_with('#') {
                continue;
            }
            if let Some((k, v)) = line.split_once('=') {
                let key = k.trim().trim_matches('"').to_string();
                if key.is_empty() {
                    continue;
                }
                if let Some(path) = inline_path(v) {
                    out.push(PathDep { key, path, kind });
                }
            }
        }
    }
    out
}

/// Path dependencies of one kind.
pub fn read_path_dependencies_of(sky_toml: &Path, kind: PathDepKind) -> Vec<PathDep> {
    read_path_dependencies(sky_toml)
        .into_iter()
        .filter(|d| d.kind == kind)
        .collect()
}

/// The `module` path a `go.mod` in `dir` declares.
pub fn go_module_path(dir: &Path) -> Option<String> {
    let text = std::fs::read_to_string(dir.join("go.mod")).ok()?;
    text.lines().find_map(|l| {
        let l = l.trim();
        let rest = l.strip_prefix("module")?;
        if !rest.starts_with(char::is_whitespace) {
            return None;
        }
        let m = rest.trim().trim_matches('"');
        (!m.is_empty()).then(|| m.to_string())
    })
}

/// Whether `dir` holds a Sky package: a `sky.toml`, or `.sky` sources under its
/// source root.
pub fn is_sky_package_dir(dir: &Path) -> bool {
    if dir.join("sky.toml").is_file() {
        return true;
    }
    let mut files = Vec::new();
    crate::build::collect_sky(&dir.join(crate::configured_source_root(dir)), &mut files);
    !files.is_empty()
}

/// The name a Sky path dependency is recorded under: the package's own
/// top-level `name` in its `sky.toml`, else the directory name.
pub fn sky_package_name(dir: &Path) -> String {
    let from_toml = std::fs::read_to_string(dir.join("sky.toml"))
        .ok()
        .and_then(|t| {
            for raw in t.lines() {
                let l = raw.trim();
                if l.starts_with('[') {
                    return None; // top-level keys only
                }
                if let Some((k, v)) = l.split_once('=') {
                    if k.trim() == "name" {
                        let v = v.trim().trim_matches('"').trim();
                        if !v.is_empty() {
                            return Some(v.to_string());
                        }
                    }
                }
            }
            None
        });
    from_toml.unwrap_or_else(|| {
        dir.file_name()
            .map(|n| n.to_string_lossy().to_string())
            .unwrap_or_else(|| "package".to_string())
    })
}

/// `target` relative to `base`, both absolute and canonical, `/`-separated
/// (`../greet`). Falls back to `target` itself when they share no root.
pub fn relative_path(base: &Path, target: &Path) -> String {
    let b: Vec<Component> = base.components().collect();
    let t: Vec<Component> = target.components().collect();
    let common = b.iter().zip(t.iter()).take_while(|(x, y)| x == y).count();
    if common == 0 {
        return target.to_string_lossy().replace('\\', "/");
    }
    let mut parts: Vec<String> = Vec::new();
    for _ in common..b.len() {
        parts.push("..".to_string());
    }
    for c in &t[common..] {
        parts.push(c.as_os_str().to_string_lossy().to_string());
    }
    if parts.is_empty() {
        ".".to_string()
    } else {
        let s = parts.join("/");
        if s.starts_with("..") {
            s
        } else {
            format!("./{s}")
        }
    }
}

/// The `sky.toml` right-hand side for a path dependency.
pub fn inline_value(path: &str) -> String {
    format!("{{ path = \"{}\" }}", path.replace('\\', "/"))
}

/// Wire every Go path dependency into the `go.mod` in `go_dir`: `require <mod>
/// v0.0.0` plus `replace <mod> => <absolute dir>`. Called on every build (the
/// build rewrites `go.mod` from the runtime's copy first) and by `sky add` /
/// `sky install`. `go mod edit` reads and writes the file only — no network.
pub fn apply_go_path_deps(project_dir: &Path, go_dir: &Path) -> Result<(), String> {
    let deps = read_path_dependencies_of(&project_dir.join("sky.toml"), PathDepKind::Go);
    if deps.is_empty() {
        return Ok(());
    }
    let mut args: Vec<String> = vec!["mod".into(), "edit".into()];
    for d in &deps {
        let dir = d.resolve(project_dir);
        if !dir.join("go.mod").is_file() {
            return Err(format!(
                "Go path dependency \"{}\" = {{ path = \"{}\" }}: {} has no go.mod",
                d.key,
                d.path,
                dir.display()
            ));
        }
        args.push(format!("-require={}@v0.0.0", d.key));
        args.push(format!("-replace={}={}", d.key, dir.to_string_lossy()));
    }
    let out = Command::new("go")
        .args(&args)
        .current_dir(go_dir)
        .output()
        .map_err(|e| format!("spawn go mod edit: {e}"))?;
    if out.status.success() {
        Ok(())
    } else {
        Err(format!(
            "go mod edit (path dependencies) failed: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ))
    }
}

/// Undo [`apply_go_path_deps`] for one module in `go_dir`'s `go.mod`.
pub fn drop_go_path_dep(go_dir: &Path, module: &str) -> Result<(), String> {
    let out = Command::new("go")
        .args([
            "mod",
            "edit",
            &format!("-droprequire={module}"),
            &format!("-dropreplace={module}"),
        ])
        .current_dir(go_dir)
        .output()
        .map_err(|e| format!("spawn go mod edit: {e}"))?;
    if out.status.success() {
        Ok(())
    } else {
        Err(String::from_utf8_lossy(&out.stderr).trim().to_string())
    }
}

/// A declared path dependency whose directory is missing — the build stops on
/// it, as it stops on a declared but unfetched Sky package.
pub fn missing(project_dir: &Path) -> Vec<(PathDep, PathBuf)> {
    read_path_dependencies(&project_dir.join("sky.toml"))
        .into_iter()
        .filter_map(|d| {
            let dir = d.resolve(project_dir);
            (!dir.is_dir()).then_some((d, dir))
        })
        .collect()
}

/// The error the build reports for [`missing`] dependencies. `None` when every
/// declared path exists.
pub fn missing_error(project_dir: &Path) -> Option<String> {
    let gone = missing(project_dir);
    if gone.is_empty() {
        return None;
    }
    let lines: Vec<String> = gone
        .iter()
        .map(|(d, dir)| {
            format!(
                "path dependency \"{}\" = {{ path = \"{}\" }} points at {}, which does not exist \
                 — restore it, or run `sky remove {}`",
                d.key,
                d.path,
                dir.display(),
                d.key
            )
        })
        .collect();
    Some(lines.join("\n"))
}

/// Drift warnings for the declared path dependencies that exist: a Go module
/// whose `go.mod` now declares a different module path than the key it was
/// added under, and a Go module whose exported API changed since its FFI
/// surface was generated ([`signature_fingerprint`]: a new or changed
/// signature is not callable until `sky install` re-inspects it; a changed
/// function BODY needs nothing — the build compiles the directory as it is).
pub fn drift_warnings(project_dir: &Path) -> Vec<String> {
    let mut out = Vec::new();
    for d in read_path_dependencies_of(&project_dir.join("sky.toml"), PathDepKind::Go) {
        let dir = d.resolve(project_dir);
        if !dir.is_dir() {
            continue; // reported by `missing_error`
        }
        match go_module_path(&dir) {
            Some(m) if m != d.key => out.push(format!(
                "path dependency \"{}\" ({}): its go.mod now declares module \"{m}\". \
                 Run `sky remove {}` then `sky add {}` to record the new module path",
                d.key, d.path, d.key, d.path
            )),
            None => out.push(format!(
                "path dependency \"{}\" ({}): {} has no go.mod",
                d.key,
                d.path,
                dir.display()
            )),
            Some(_) => {
                let recorded = recorded_signature(project_dir, &d.key);
                if recorded.is_some_and(|r| r != signature_fingerprint(&dir)) {
                    out.push(format!(
                        "path dependency \"{}\" ({}) changed its exported Go API since its \
                         FFI surface was generated. The new or changed functions are not \
                         callable until you run `sky install`",
                        d.key, d.path
                    ));
                }
            }
        }
    }
    out
}

/// A fingerprint of a Go package's EXPORTED API as its source spells it: every
/// top-level `func`, `type`, `var` and `const` line that declares an exported
/// name, up to its opening brace, from the non-test `.go` files directly in
/// `dir` (the package `sky add` inspected is the module root). A function
/// BODY edit leaves it unchanged, so it drives the "run `sky install`" warning
/// without crying wolf on every save; a new function or a changed signature
/// moves it.
pub fn signature_fingerprint(dir: &Path) -> String {
    let mut files: Vec<PathBuf> = std::fs::read_dir(dir)
        .map(|rd| {
            rd.filter_map(|e| e.ok().map(|e| e.path()))
                .filter(|p| {
                    let n = p.file_name().map(|n| n.to_string_lossy().to_string());
                    n.is_some_and(|n| n.ends_with(".go") && !n.ends_with("_test.go"))
                })
                .collect()
        })
        .unwrap_or_default();
    files.sort();
    let mut h = crate::sha256::Sha256::new();
    for f in files {
        let Ok(text) = std::fs::read_to_string(&f) else {
            continue;
        };
        for line in text.lines() {
            let head = line.split('{').next().unwrap_or(line).trim_end();
            let decl = ["func ", "type ", "var ", "const "]
                .iter()
                .find_map(|kw| head.strip_prefix(kw));
            let Some(rest) = decl else {
                continue;
            };
            // `func (r *T) Name(` → the name after the receiver.
            let rest = if rest.starts_with('(') {
                rest.split_once(')')
                    .map(|(_, r)| r.trim_start())
                    .unwrap_or("")
            } else {
                rest
            };
            if rest.chars().next().is_some_and(char::is_uppercase) {
                h.update(head.as_bytes());
                h.update(b"\n");
            }
        }
    }
    h.finish().iter().map(|b| format!("{b:02x}")).collect()
}

/// Where [`record_signature`] keeps a Go path dependency's fingerprint: beside
/// its generated surface, `sky-ffi/<slug>.pathsig` (a build artefact, like the
/// rest of `sky-ffi/`).
fn signature_file(project_dir: &Path, module: &str) -> Option<PathBuf> {
    let slug = crate::ffi_ops::slug_for_package(project_dir, module)?;
    Some(project_dir.join("sky-ffi").join(format!("{slug}.pathsig")))
}

/// Record the fingerprint of `dir`'s exported API next to the surface just
/// generated for `module` (`sky add`, `sky install`).
pub fn record_signature(project_dir: &Path, module: &str, dir: &Path) -> Result<(), String> {
    let Some(f) = signature_file(project_dir, module) else {
        return Err(format!("no generated surface for {module}"));
    };
    std::fs::write(&f, signature_fingerprint(dir))
        .map_err(|e| format!("write {}: {e}", f.display()))
}

fn recorded_signature(project_dir: &Path, module: &str) -> Option<String> {
    std::fs::read_to_string(signature_file(project_dir, module)?)
        .ok()
        .map(|s| s.trim().to_string())
}

/// The source directories of every Sky path dependency that exists (each
/// package's configured source root, default `src`).
pub fn sky_source_dirs(project_dir: &Path) -> Vec<PathBuf> {
    read_path_dependencies_of(&project_dir.join("sky.toml"), PathDepKind::Sky)
        .into_iter()
        .map(|d| d.resolve(project_dir))
        .filter(|dir| dir.is_dir())
        .map(|dir| {
            let root = crate::configured_source_root(&dir);
            dir.join(root)
        })
        .collect()
}

/// Rewrite every relative `path` in a `sky.toml` text to an absolute one,
/// resolved against `project_dir`. Used when a build copies the manifest into
/// a generated project (the Std.App derived entry under `.skyapp/`, a Sky.Spa
/// split leg), whose root is a different directory: a relative path copied
/// verbatim would resolve against the wrong root.
pub fn absolutize_manifest(text: &str, project_dir: &Path) -> String {
    let mut in_dep_section = false;
    let mut out: Vec<String> = Vec::new();
    for raw in text.lines() {
        let t = raw.trim();
        if t.starts_with('[') && t.ends_with(']') {
            in_dep_section = section_matches(t, PathDepKind::Go.section())
                || section_matches(t, PathDepKind::Sky.section());
            out.push(raw.to_string());
            continue;
        }
        if in_dep_section {
            if let Some((k, v)) = raw.split_once('=') {
                if let Some(path) = inline_path(v) {
                    if !Path::new(&path).is_absolute() {
                        let dep = PathDep {
                            key: String::new(),
                            path,
                            kind: PathDepKind::Sky,
                        };
                        let abs = dep.resolve(project_dir);
                        out.push(format!(
                            "{} = {}",
                            k.trim_end(),
                            inline_value(&abs.to_string_lossy())
                        ));
                        continue;
                    }
                }
            }
        }
        out.push(raw.to_string());
    }
    let mut s = out.join("\n");
    if text.ends_with('\n') {
        s.push('\n');
    }
    s
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!(
            "sky-path-deps-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn inline_path_reads_the_path_key_only() {
        assert_eq!(inline_path(r#"{ path = "../x" }"#).as_deref(), Some("../x"));
        assert_eq!(
            inline_path(r#"{path="/abs/y", other = "z"}"#).as_deref(),
            Some("/abs/y")
        );
        assert_eq!(inline_path(r#""v1.2.3""#), None);
        assert_eq!(inline_path(r#"{ version = "v1" }"#), None);
    }

    #[test]
    fn path_args_are_recognised() {
        for a in [".", "..", "./x", "../x/y", "/abs/dir"] {
            assert!(is_path_arg(a), "{a}");
        }
        for a in ["github.com/x/y", "net/http", "x"] {
            assert!(!is_path_arg(a), "{a}");
        }
    }

    #[test]
    fn relative_path_walks_up_and_down() {
        assert_eq!(
            relative_path(Path::new("/a/b/proj"), Path::new("/a/b/greet")),
            "../greet"
        );
        assert_eq!(
            relative_path(Path::new("/a/b/proj"), Path::new("/a/b/proj/libs/x")),
            "./libs/x"
        );
        assert_eq!(relative_path(Path::new("/a"), Path::new("/a")), ".");
    }

    #[test]
    fn reads_both_sections_and_resolves_against_the_project_root() {
        let dir = scratch("read");
        let toml = dir.join("sky.toml");
        std::fs::write(
            &toml,
            "name = \"x\"\n\n[dependencies]\n\"github.com/a/remote\" = \"v1.0.0\"\n\"widgets\" = { path = \"../widgets\" }\n\n[\"go.dependencies\"]\n\"example.com/greet\" = { path = \"./vendor/greet\" }\n\"github.com/google/uuid\" = \"v1.6.0\"\n",
        )
        .unwrap();
        let deps = read_path_dependencies(&toml);
        assert_eq!(deps.len(), 2, "{deps:?}");
        assert_eq!(deps[0].kind, PathDepKind::Go);
        assert_eq!(deps[0].key, "example.com/greet");
        assert_eq!(deps[1].kind, PathDepKind::Sky);
        assert_eq!(deps[1].resolve(&dir), dir.parent().unwrap().join("widgets"));
        assert_eq!(deps[0].resolve(&dir), dir.join("vendor").join("greet"));
        // The version readers must NOT see the path entries.
        assert_eq!(
            crate::ffi_ops::read_sky_dependencies(&toml),
            vec![("github.com/a/remote".to_string(), "v1.0.0".to_string())]
        );
        assert_eq!(
            crate::ffi_ops::read_go_dependencies(&toml),
            vec![("github.com/google/uuid".to_string(), "v1.6.0".to_string())]
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn absolutize_manifest_rewrites_relative_paths_only() {
        let root = Path::new("/p/app");
        let text = "name = \"x\"\n[dependencies]\n\"w\" = { path = \"../w\" }\n\"r\" = \"v1\"\n[\"go.dependencies\"]\n\"example.com/g\" = { path = \"/abs/g\" }\n";
        let out = absolutize_manifest(text, root);
        assert!(out.contains("\"w\" = { path = \"/p/w\" }"), "{out}");
        assert!(out.contains("\"r\" = \"v1\""), "{out}");
        assert!(
            out.contains("\"example.com/g\" = { path = \"/abs/g\" }"),
            "{out}"
        );
        assert!(out.ends_with('\n'));
    }

    #[test]
    fn signature_fingerprint_ignores_bodies_and_sees_signatures() {
        let dir = scratch("sig");
        let src = |body: &str, extra: &str| {
            format!(
                "package g\n\nfunc Hello(n string) string {{\n\treturn \"{body}\" + n\n}}\n\nfunc helper() {{}}\n{extra}"
            )
        };
        std::fs::write(dir.join("g.go"), src("hi ", "")).unwrap();
        let a = signature_fingerprint(&dir);
        std::fs::write(dir.join("g.go"), src("HOWDY ", "")).unwrap();
        assert_eq!(
            a,
            signature_fingerprint(&dir),
            "a body edit must not move it"
        );
        std::fs::write(dir.join("g.go"), src("hi ", "\nfunc helper2() {}\n")).unwrap();
        assert_eq!(
            a,
            signature_fingerprint(&dir),
            "an unexported func must not move it"
        );
        std::fs::write(
            dir.join("g.go"),
            src("hi ", "\nfunc Bye() string { return \"\" }\n"),
        )
        .unwrap();
        assert_ne!(
            a,
            signature_fingerprint(&dir),
            "a new exported func must move it"
        );
        std::fs::write(dir.join("g_test.go"), "package g\n\nfunc TestX() {}\n").unwrap();
        let b = signature_fingerprint(&dir);
        std::fs::remove_file(dir.join("g_test.go")).unwrap();
        assert_eq!(b, signature_fingerprint(&dir), "test files are not the API");
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// The LSP and the build enumerate dependency sources through ONE function;
    /// a Sky path dependency's modules must be in it, from its source root.
    #[test]
    fn dependency_files_include_sky_path_dependencies() {
        let base = scratch("enum");
        let app = base.join("app");
        let lib = base.join("lib");
        std::fs::create_dir_all(app.join("src")).unwrap();
        std::fs::create_dir_all(lib.join("code/W")).unwrap();
        std::fs::write(
            lib.join("sky.toml"),
            "name = \"lib\"\n\n[source]\nroot = \"code\"\n",
        )
        .unwrap();
        std::fs::write(lib.join("code/W/X.sky"), "module W.X exposing (x)\n").unwrap();
        std::fs::write(
            app.join("sky.toml"),
            "name = \"app\"\n[dependencies]\n\"lib\" = { path = \"../lib\" }\n",
        )
        .unwrap();
        let files = crate::enumerate_dependency_files(&app);
        assert_eq!(files, vec![lib.join("code/W/X.sky")], "{files:?}");
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn go_module_path_reads_the_module_line() {
        let dir = scratch("gomod");
        std::fs::write(dir.join("go.mod"), "module example.com/greet\n\ngo 1.22\n").unwrap();
        assert_eq!(go_module_path(&dir).as_deref(), Some("example.com/greet"));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn missing_dirs_are_an_error_naming_the_dependency() {
        let dir = scratch("missing");
        std::fs::write(
            dir.join("sky.toml"),
            "name = \"x\"\n[dependencies]\n\"gone\" = { path = \"./nope\" }\n",
        )
        .unwrap();
        let e = missing_error(&dir).expect("missing path must be reported");
        assert!(e.contains("\"gone\""), "{e}");
        assert!(e.contains("does not exist"), "{e}");
        std::fs::create_dir_all(dir.join("nope")).unwrap();
        assert!(missing_error(&dir).is_none());
        let _ = std::fs::remove_dir_all(&dir);
    }
}
