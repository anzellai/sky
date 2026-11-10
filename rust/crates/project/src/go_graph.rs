//! go_graph.rs — the Go module graph `sky install` prepares and every build
//! of the project then uses.
//!
//! A build composes its `go.mod` from two sources: the runtime's own
//! requirements (`runtime-go/go.mod`) and the project's Go dependencies
//! (`["go.dependencies"]`, each fetched with `go get <module>@<spec>`, plus the
//! Go path dependencies). Minimal version selection over the two can raise a
//! module the RUNTIME imports: a newer `google.golang.org/api` requires a newer
//! `go.opentelemetry.io/proto/otlp` than `runtime-go/go.mod` pins, and the
//! build then compiles `rt/telemetry` against that newer version.
//!
//! `sky install` used to resolve the project's dependencies in `sky-out/`
//! without the runtime's packages present. It fetched what the dependencies
//! themselves import, never the raised versions of the runtime's own imports,
//! and a `"latest"` spec re-resolved on every build. So the first build after
//! an install downloaded modules: it failed on an offline module cache, an
//! air-gapped host, or a CI job that builds with `GOPROXY=file://…`.
//!
//! The fix is one graph, made once:
//!
//! * [`prepare`] (run by `sky install` and `sky update`) composes the go.mod a
//!   build composes, in `sky-out/.sky-gograph/`, materialises the runtime and
//!   the project's FFI wrappers beside it, and loads every package a build
//!   compiles with `go list -deps` for the host and for `js/wasm` (the Sky.Spa
//!   client). That downloads each module the build needs, at the version the
//!   build selects, and records their checksums in `go.sum`. The result and a
//!   fingerprint of its inputs ([`fingerprint`]) are kept.
//! * [`apply`] (run by every build, for every leg: Sky.Live, a Std.App
//!   staging under `.skyapp/`, a Sky.Spa backend or wasm client under
//!   `.split/`) starts the build's go.mod from that recorded graph instead of
//!   the runtime's copy, and [`recorded_version`] gives a floating spec
//!   (`"latest"`, a branch) the version install resolved. The build then needs
//!   nothing the install did not fetch, and two builds between installs use
//!   the same versions. `"latest"` re-floats on `sky install` / `sky update`.
//!
//! A record whose inputs changed (a new Sky, an edited `sky.toml`, a path
//! dependency's go.mod) is not used: the build composes from the runtime's
//! copy and resolves the specs itself, as it did before the record existed.

use std::path::{Path, PathBuf};
use std::process::Command;

/// The recorded graph, inside the project's `sky-out/`.
pub const DIR: &str = ".sky-gograph";

/// The fingerprint file, written last (a partial prepare leaves none).
const INPUTS: &str = "inputs";

/// The project a build directory belongs to. A generated leg lives under the
/// user's project (`.skyapp/<target>/`, `.split/<role>/`, or both nested), so
/// its project is the directory above the OUTERMOST such component. Any other
/// directory is its own project.
pub fn project_root_of(dir: &Path) -> PathBuf {
    let abs = dir.canonicalize().unwrap_or_else(|_| dir.to_path_buf());
    let mut root: Option<PathBuf> = None;
    for a in abs.ancestors() {
        let name = a.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if matches!(name, ".skyapp" | ".split") {
            if let Some(p) = a.parent() {
                if p.join("sky.toml").is_file() {
                    root = Some(p.to_path_buf());
                }
            }
        }
    }
    root.unwrap_or(abs)
}

/// Whether `path` names an external module (its first segment has a dot).
fn is_external(path: &str) -> bool {
    path.split('/')
        .next()
        .is_some_and(|head| head.contains('.'))
}

/// The external Go dependencies `sky.toml` declares, as `(key, spec)`.
fn declared(project_root: &Path) -> Vec<(String, String)> {
    crate::ffi_ops::read_go_dependencies(&project_root.join("sky.toml"))
        .into_iter()
        .filter(|(k, _)| is_external(k))
        .collect()
}

/// Whether the project has anything for [`prepare`] to resolve: an external Go
/// dependency or a Go path dependency.
pub fn has_go_dependencies(project_root: &Path) -> bool {
    !declared(project_root).is_empty()
        || !crate::path_deps::read_path_dependencies_of(
            &project_root.join("sky.toml"),
            crate::path_deps::PathDepKind::Go,
        )
        .is_empty()
}

/// A hash of everything the recorded graph is a function of: this Sky, the
/// runtime's go.mod and go.sum, each declared Go dependency and its spec, and
/// each Go path dependency's directory and go.mod.
pub fn fingerprint(project_root: &Path, repo_root: &Path) -> String {
    let mut h = crate::sha256::Sha256::new();
    let mut put = |label: &str, v: &[u8]| {
        h.update(label.as_bytes());
        h.update(&(v.len() as u64).to_le_bytes());
        h.update(v);
    };
    put("sky", env!("CARGO_PKG_VERSION").as_bytes());
    let rt = repo_root.join("runtime-go");
    put(
        "rt.go.mod",
        &std::fs::read(rt.join("go.mod")).unwrap_or_default(),
    );
    put(
        "rt.go.sum",
        &std::fs::read(rt.join("go.sum")).unwrap_or_default(),
    );
    let mut deps = declared(project_root);
    deps.sort();
    for (k, spec) in &deps {
        put(
            "dep",
            format!("{k}={}", crate::ffi_ops::spec_or_latest(spec)).as_bytes(),
        );
    }
    let mut paths = crate::path_deps::read_path_dependencies_of(
        &project_root.join("sky.toml"),
        crate::path_deps::PathDepKind::Go,
    );
    paths.sort_by(|a, b| a.key.cmp(&b.key));
    for d in &paths {
        let dir = d.resolve(project_root);
        put("path", format!("{}={}", d.key, dir.display()).as_bytes());
        put(
            "path.go.mod",
            &std::fs::read(dir.join("go.mod")).unwrap_or_default(),
        );
    }
    h.finish()[..16]
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

/// The platforms whose packages [`prepare`] loads: the host as the
/// environment configures it (`GOOS` / `GOARCH`, with cgo on so the desktop
/// shell's cgo files count), and `js/wasm` (the Sky.Spa client).
fn platforms() -> Vec<Vec<(&'static str, &'static str)>> {
    vec![
        vec![("CGO_ENABLED", "1")],
        vec![("GOOS", "js"), ("GOARCH", "wasm"), ("CGO_ENABLED", "0")],
    ]
}

fn go(dir: &Path, args: &[&str], env: &[(&str, &str)]) -> Result<String, String> {
    let mut c = Command::new("go");
    c.args(args)
        .current_dir(dir)
        .env("GOFLAGS", crate::go_cmd::goflags())
        .stdin(std::process::Stdio::null());
    for (k, v) in env {
        c.env(k, v);
    }
    let out = c
        .output()
        .map_err(|e| format!("spawn go {}: {e}", args.join(" ")))?;
    if out.status.success() {
        Ok(String::from_utf8_lossy(&out.stdout).into_owned())
    } else {
        Err(format!(
            "go {} failed: {}",
            args.join(" "),
            String::from_utf8_lossy(&out.stderr).trim()
        ))
    }
}

/// Compose the build's module graph and download every module a build of the
/// project needs (see the module docs). Returns one line describing the
/// result. On an error nothing is recorded, so a build resolves by itself.
pub fn prepare(project_root: &Path, repo_root: &Path) -> Result<String, String> {
    let dir = project_root.join("sky-out").join(DIR);
    if dir.exists() {
        std::fs::remove_dir_all(&dir).map_err(|e| format!("clear {}: {e}", dir.display()))?;
    }
    std::fs::create_dir_all(&dir).map_err(|e| format!("mkdir {}: {e}", dir.display()))?;

    // 1. The go.mod / go.sum a build starts from, and the runtime it compiles.
    let rt = repo_root.join("runtime-go");
    std::fs::copy(rt.join("go.mod"), dir.join("go.mod"))
        .map_err(|e| format!("copy runtime go.mod: {e}"))?;
    if rt.join("go.sum").is_file() {
        std::fs::copy(rt.join("go.sum"), dir.join("go.sum"))
            .map_err(|e| format!("copy runtime go.sum: {e}"))?;
    }
    crate::build::materialise_rt_for_graph(&rt.join("rt"), &dir.join("rt"))
        .map_err(|e| format!("materialise the runtime: {e}"))?;

    // 2. The project's dependencies, in the order a build applies them.
    crate::path_deps::apply_go_path_deps(project_root, &dir)?;
    let mut targets: Vec<String> = Vec::new();
    for (k, spec) in declared(project_root) {
        crate::ffi_ops::validate_spec(&k, &spec)?;
        targets.push(format!("{k}@{}", crate::ffi_ops::spec_or_latest(&spec)));
    }
    if !targets.is_empty() {
        let mut args: Vec<&str> = vec!["get"];
        args.extend(targets.iter().map(String::as_str));
        go(&dir, &args, &[])?;
    }

    // 3. Every FFI wrapper the project has, so its imports load too.
    let ffi_go = project_root.join("sky-ffi").join("go");
    if let Ok(rd) = std::fs::read_dir(&ffi_go) {
        let skyffi = dir.join("skyffi");
        for e in rd.flatten() {
            let name = e.file_name();
            if name.to_string_lossy().ends_with("_bindings.go") {
                std::fs::create_dir_all(&skyffi).map_err(|e| format!("mkdir skyffi: {e}"))?;
                std::fs::copy(e.path(), skyffi.join(&name))
                    .map_err(|e| format!("copy {}: {e}", name.to_string_lossy()))?;
            }
        }
    }

    // 4. Load every package a build compiles, per platform. Loading fetches
    //    each module at the selected version and records its checksum; the
    //    explicit `go mod download` then fails loudly on one that did not
    //    arrive (`-e` lets a package that does not build for one platform
    //    through, never a missing module).
    let mut modules: std::collections::BTreeSet<String> = std::collections::BTreeSet::new();
    for env in platforms() {
        let listed = go(
            &dir,
            &[
                "list",
                "-e",
                "-deps",
                "-f",
                "{{with .Module}}{{if not .Main}}{{if not .Replace}}{{.Path}}@{{.Version}}{{end}}{{end}}{{end}}",
                "./...",
            ],
            &env,
        )?;
        modules.extend(
            listed
                .lines()
                .map(str::trim)
                .filter(|l| !l.is_empty() && !l.ends_with('@'))
                .map(String::from),
        );
    }
    if !modules.is_empty() {
        let mut args: Vec<&str> = vec!["mod", "download"];
        args.extend(modules.iter().map(String::as_str));
        go(&dir, &args, &[])?;
    }

    std::fs::write(dir.join(INPUTS), fingerprint(project_root, repo_root))
        .map_err(|e| format!("write {}/{INPUTS}: {e}", DIR))?;
    Ok(format!(
        "prepared the build's Go module graph ({} modules downloaded) in sky-out/{DIR}",
        modules.len()
    ))
}

/// Start the go.mod / go.sum in `go_dir` from the graph `sky install`
/// recorded for the project `build_dir` belongs to, when its inputs are
/// unchanged. Returns the recorded go.mod text when it was applied.
pub fn apply(build_dir: &Path, repo_root: &Path, go_dir: &Path) -> Option<String> {
    let root = project_root_of(build_dir);
    let dir = root.join("sky-out").join(DIR);
    let recorded = std::fs::read_to_string(dir.join(INPUTS)).ok()?;
    if recorded.trim() != fingerprint(&root, repo_root) {
        return None;
    }
    let go_mod = std::fs::read_to_string(dir.join("go.mod")).ok()?;
    let go_sum = std::fs::read_to_string(dir.join("go.sum")).unwrap_or_default();
    std::fs::write(go_dir.join("go.mod"), &go_mod).ok()?;
    std::fs::write(go_dir.join("go.sum"), go_sum).ok()?;
    Some(go_mod)
}

/// The version `go_mod` requires for the module that owns `path` (the
/// `require` whose module path is the longest prefix of `path`, so a package
/// key such as `google.golang.org/api/iterator` maps to its module).
pub fn recorded_version(go_mod: &str, path: &str) -> Option<String> {
    let mut best: Option<(usize, String)> = None;
    for line in go_mod.lines() {
        let l = line.trim();
        let l = l.strip_prefix("require").map_or(l, str::trim);
        let mut it = l.split_whitespace();
        let (Some(module), Some(version)) = (it.next(), it.next()) else {
            continue;
        };
        if !version.starts_with('v') {
            continue;
        }
        let owns = path == module || path.starts_with(&format!("{module}/"));
        if owns && best.as_ref().is_none_or(|(len, _)| module.len() > *len) {
            best = Some((module.len(), version.to_string()));
        }
    }
    best.map(|(_, v)| v)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn recorded_version_takes_the_longest_owning_module() {
        let go_mod = "module sky-app\n\ngo 1.25.0\n\nrequire (\n\tgoogle.golang.org/api v0.311.0\n\
                      \tgoogle.golang.org/api/extra v0.2.0 // indirect\n)\n\
                      require github.com/google/uuid v1.6.0\n";
        assert_eq!(
            recorded_version(go_mod, "google.golang.org/api/iterator").as_deref(),
            Some("v0.311.0")
        );
        assert_eq!(
            recorded_version(go_mod, "google.golang.org/api/extra/x").as_deref(),
            Some("v0.2.0")
        );
        assert_eq!(
            recorded_version(go_mod, "github.com/google/uuid").as_deref(),
            Some("v1.6.0")
        );
        assert_eq!(recorded_version(go_mod, "github.com/google/uuidx"), None);
        // `go 1.25.0` and `module sky-app` are not requirements.
        assert_eq!(recorded_version(go_mod, "go"), None);
    }

    #[test]
    fn a_generated_leg_belongs_to_the_project_above_it() {
        let base = std::env::temp_dir().join(format!(
            "sky-go-graph-root-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let leg = base.join(".skyapp/web-app/.split/backend");
        std::fs::create_dir_all(&leg).unwrap();
        std::fs::write(base.join("sky.toml"), "name = \"x\"\n").unwrap();
        std::fs::write(base.join(".skyapp/web-app/sky.toml"), "name = \"x\"\n").unwrap();
        std::fs::write(leg.join("sky.toml"), "name = \"x\"\n").unwrap();
        let root = base.canonicalize().unwrap();
        assert_eq!(project_root_of(&leg), root);
        assert_eq!(project_root_of(&base), root);
        let _ = std::fs::remove_dir_all(&base);
    }
}
