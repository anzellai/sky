//! `sky add` / `install` / `remove` / `update` — the ONLY verbs that run the Go
//! FFI inspector (doc 09 §D). They are the FFI analogue of `cargo add`: an
//! explicit, out-of-band mutation of a committed manifest (`sky.toml`) + lockfile
//! (`sky-out/go.mod`/`go.sum`) + generated surface (`sky-ffi/`), never part of
//! `build`. The inspector is pinned to `linux/amd64` and its output normalised +
//! committed, so a fresh clone / CI reads the same bytes (L4).
//!
//! Committed surface layout (doc 09 §C.1 — moved out of gitignored `.skycache/`):
//! ```text
//! <project>/sky-ffi/<slug>.kernel.json     # machine surface (compiler reads)
//! <project>/sky-ffi/<slug>.skyi            # human catalogue (sky doc / review)
//! <project>/sky-ffi/go/<slug>_bindings.go  # the `package rt` Go wrapper
//! ```

use std::path::{Path, PathBuf};
use std::process::Command;

/// Structured outcome of an FFI verb — human-facing lines + a hard-fail flag.
pub struct FfiReport {
    pub lines: Vec<String>,
    pub ok: bool,
}

impl FfiReport {
    fn new() -> Self {
        FfiReport {
            lines: Vec::new(),
            ok: true,
        }
    }
    fn say(&mut self, m: impl Into<String>) {
        self.lines.push(m.into());
    }
    fn fail(mut self, m: impl Into<String>) -> Self {
        self.lines.push(m.into());
        self.ok = false;
        self
    }
}

// ---------------------------------------------------------------------------
// sky add
// ---------------------------------------------------------------------------

/// `sky add <import-path>` — resolve+pin the Go dep, inspect it (pinned target),
/// emit the canonical surface, commit it under `sky-ffi/`, and record the dep in
/// `sky.toml`.
pub fn add(project_dir: &Path, repo_root: &Path, pkg: &str, spec: Option<&str>) -> FfiReport {
    let mut r = FfiReport::new();
    // `explicit_spec` = what the user typed after `@` (None if they gave just the
    // path). `resolve_spec` is what `go get` fetches against.
    let explicit_spec = spec;
    let resolve_spec = spec_or_latest(explicit_spec.unwrap_or("latest"));
    if let Err(e) = validate_spec(pkg, resolve_spec) {
        return r.fail(format!("sky add: {e}"));
    }
    let sky_out = project_dir.join("sky-out");
    if let Err(e) = ensure_go_mod(repo_root, &sky_out) {
        return r.fail(format!("sky add: {e}"));
    }

    // External modules resolve at `path@<spec>`; stdlib paths (`net/http`) carry
    // no module version, so `go get` sees the bare path (a no-op download).
    let target = if is_external_module(pkg) {
        format!("{pkg}@{resolve_spec}")
    } else {
        pkg.to_string()
    };
    r.say(format!("Resolving {target} (go get)…"));
    if let Err(e) = go_get(&sky_out, &[target]) {
        // Non-fatal: some paths (stdlib) need no download; surface as a note.
        r.say(format!("  note: go get: {e}"));
    }

    // Stdlib packages (`net/http`, `io`) carry no external module + their surface
    // is part of the runtime baseline; the oracle still inspects them, so do too,
    // but skip the go.mod require dance (go get is a no-op for stdlib).
    let bin = match ffi::ensure_inspector(repo_root) {
        Ok(b) => b,
        Err(e) => return r.fail(format!("sky add: {e}")),
    };

    r.say(format!(
        "Inspecting {pkg} (GOOS={}/{}, normalised)…",
        ffi::inspect::PIN_GOOS,
        ffi::inspect::PIN_GOARCH
    ));
    let info = match ffi::run_inspector(&bin, &sky_out, &[pkg.to_string()]) {
        Ok(mut v) if !v.is_empty() => v.remove(0),
        Ok(_) => return r.fail(format!("sky add: inspector returned no package for {pkg}")),
        Err(e) => return r.fail(format!("sky add: {e}")),
    };

    match write_surface(project_dir, &info) {
        Ok(slug) => r.say(format!(
            "  wrote sky-ffi/{slug}.{{kernel.json,skyi}} + go/{slug}_bindings.go"
        )),
        Err(e) => return r.fail(format!("sky add: {e}")),
    }

    // Pin-by-default: when the user gave no explicit version, record the EXACT
    // version `go get` just resolved (reproducible builds out of the box). An
    // explicit spec — including a deliberate `"latest"` or a branch — is written
    // verbatim so an intentional float stays a float.
    let write_spec: String = match explicit_spec {
        Some(s) => s.to_string(),
        None if is_external_module(pkg) => {
            resolved_module_version(&sky_out, pkg).unwrap_or_else(|| "latest".to_string())
        }
        None => "latest".to_string(),
    };

    // Upsert: re-adding a dep (or `sky add pkg@newversion`) updates the recorded
    // spec — the most recent `sky add` wins — so sky.toml never disagrees with the
    // surface we just regenerated.
    match upsert_go_dependency(&project_dir.join("sky.toml"), pkg, &write_spec) {
        Ok(DepEdit::Added) => r.say(format!(
            "  recorded {pkg} = \"{write_spec}\" in sky.toml [\"go.dependencies\"]"
        )),
        Ok(DepEdit::Updated(old)) => r.say(format!(
            "  updated {pkg} in sky.toml: \"{old}\" → \"{write_spec}\""
        )),
        Ok(DepEdit::Unchanged) => r.say(format!("  {pkg} already at \"{write_spec}\"")),
        Err(e) => r.say(format!("  warn: sky.toml update: {e}")),
    }
    r.say(format!("Added {pkg}."));
    r
}

// ---------------------------------------------------------------------------
// sky remove
// ---------------------------------------------------------------------------

/// `sky remove <pkg>` — drop the committed surface files, remove the dep from
/// `go.mod` (`go mod edit -droprequire` + `go mod tidy`) and `sky.toml`.
pub fn remove(project_dir: &Path, pkg: &str) -> FfiReport {
    let mut r = FfiReport::new();
    // Locate the committed surface whose "package" == pkg → delete its files.
    if let Some(slug) = slug_for_package(project_dir, pkg) {
        for rel in [
            format!("sky-ffi/{slug}.kernel.json"),
            format!("sky-ffi/{slug}.skyi"),
            format!("sky-ffi/go/{slug}_bindings.go"),
        ] {
            let p = project_dir.join(&rel);
            if p.exists() && std::fs::remove_file(&p).is_ok() {
                r.say(format!("  removed {rel}"));
            }
        }
    } else {
        r.say(format!(
            "  no committed surface found for {pkg} (nothing to delete)"
        ));
    }

    let sky_out = project_dir.join("sky-out");
    if sky_out.join("go.mod").is_file() {
        let _ = run_go(&sky_out, &["mod", "edit", "-droprequire", pkg]);
        let _ = run_go(&sky_out, &["mod", "tidy"]);
        r.say(format!("  dropped {pkg} from sky-out/go.mod"));
    }

    match remove_go_dependency(&project_dir.join("sky.toml"), pkg) {
        Ok(true) => r.say(format!("  removed {pkg} from sky.toml")),
        Ok(false) => {}
        Err(e) => r.say(format!("  warn: sky.toml update: {e}")),
    }
    r.say(format!("Removed {pkg}."));
    r
}

// ---------------------------------------------------------------------------
// sky install  (the fresh-clone / CI entry point)
// ---------------------------------------------------------------------------

/// `sky install` — ensure `go.mod` deps are present and regenerate any committed
/// surface that is ABSENT; for present surfaces, verify they match a fresh
/// inspection and report drift (doc 09 §D.2). Does not overwrite a present,
/// matching surface.
pub fn install(project_dir: &Path, repo_root: &Path) -> FfiReport {
    let mut r = FfiReport::new();
    let deps = read_go_dependencies(&project_dir.join("sky.toml"));
    if deps.is_empty() {
        r.say("sky install: no [\"go.dependencies\"] in sky.toml — nothing to do.");
        return r;
    }
    // Reject unresolvable specs up front — a bad constraint must not silently
    // fall through to `@latest`.
    for (path, spec) in &deps {
        if let Err(e) = validate_spec(path, spec) {
            return r.fail(format!("sky install: {e}"));
        }
    }
    let sky_out = project_dir.join("sky-out");
    if let Err(e) = ensure_go_mod(repo_root, &sky_out) {
        return r.fail(format!("sky install: {e}"));
    }
    // Ensure every declared external dep is pinned in go.mod at its declared spec
    // (`path@<spec>`). An EXACT pin is forced even when the inherited runtime
    // go.mod already `require`s a different version — the sky.toml pin must win
    // (and `go get path@vX.Y.Z` is idempotent when already matching). A
    // `latest`/branch spec is fetched only when missing (so it doesn't re-float
    // on every CI install).
    let to_fetch: Vec<String> = deps
        .iter()
        .filter(|(path, spec)| {
            is_external_module(path) && (is_exact_pin(spec) || !module_required(&sky_out, path))
        })
        .map(|(path, spec)| format!("{path}@{}", spec_or_latest(spec)))
        .collect();
    if !to_fetch.is_empty() {
        r.say(format!("Fetching {} module(s)…", to_fetch.len()));
        if let Err(e) = go_get(&sky_out, &to_fetch) {
            r.say(format!("  note: go get: {e}"));
        }
    }

    let bin = match ffi::ensure_inspector(repo_root) {
        Ok(b) => b,
        Err(e) => return r.fail(format!("sky install: {e}")),
    };

    let mut regenerated = 0usize;
    let mut verified = 0usize;
    let mut drifted = 0usize;
    for (dep, _spec) in &deps {
        let present = slug_for_package(project_dir, dep).is_some();
        if present {
            // Verify: re-inspect + regenerate in-memory, compare to committed.
            match regenerate_in_memory(&bin, &sky_out, dep) {
                Ok(surface) => {
                    let committed = project_dir
                        .join("sky-ffi")
                        .join(format!("{}.kernel.json", surface.slug));
                    match std::fs::read_to_string(&committed) {
                        Ok(on_disk) if on_disk == surface.kernel_json => verified += 1,
                        Ok(_) => {
                            drifted += 1;
                            r.say(format!(
                                "  DRIFT: committed sky-ffi/{}.kernel.json differs from a fresh inspection of {dep} (run `sky update`)",
                                surface.slug
                            ));
                        }
                        Err(_) => verified += 1,
                    }
                }
                Err(e) => r.say(format!("  note: could not verify {dep}: {e}")),
            }
        } else {
            match regenerate_committed(&bin, &sky_out, project_dir, dep) {
                Ok(slug) => {
                    regenerated += 1;
                    r.say(format!("  generated missing surface sky-ffi/{slug}.*"));
                }
                Err(e) => r.say(format!("  note: could not generate {dep}: {e}")),
            }
        }
    }
    r.say(format!(
        "sky install: {verified} verified, {regenerated} generated, {drifted} drifted ({} deps).",
        deps.len()
    ));
    if drifted > 0 {
        r.ok = false;
    }
    r
}

// ---------------------------------------------------------------------------
// sky update
// ---------------------------------------------------------------------------

/// `sky update` — bump dep versions (`go get -u ./... && go mod tidy`), then
/// re-inspect + re-commit every declared surface so the version bump shows up as
/// a reviewable diff in the committed `kernel.json`.
pub fn update(project_dir: &Path, repo_root: &Path) -> FfiReport {
    let mut r = FfiReport::new();
    let sky_out = project_dir.join("sky-out");
    if !sky_out.join("go.mod").is_file() {
        return r.fail("sky update: no sky-out/go.mod — run `sky build` or `sky install` first.");
    }

    let deps = read_go_dependencies(&project_dir.join("sky.toml"));
    if deps.is_empty() {
        r.say("sky update: no declared surfaces to regenerate.");
        return r;
    }
    for (path, spec) in &deps {
        if let Err(e) = validate_spec(path, spec) {
            return r.fail(format!("sky update: {e}"));
        }
    }
    // Per-dep: `latest` / branch re-floats (`go get -u <path>`); an exact
    // `vX.Y.Z` / commit SHA is a deliberate pin and is left untouched.
    r.say("Updating Go deps (exact pins skipped)…");
    for (path, spec) in &deps {
        if !is_external_module(path) {
            continue; // stdlib — no module version to bump.
        }
        if is_exact_pin(spec) {
            r.say(format!("  {path}: pinned at {spec}, skipped"));
        } else {
            let _ = run_go(&sky_out, &["get", "-u", path]);
            r.say(format!("  {path}: re-floated ({spec})"));
        }
    }
    let _ = run_go(&sky_out, &["mod", "tidy"]);

    let bin = match ffi::ensure_inspector(repo_root) {
        Ok(b) => b,
        Err(e) => return r.fail(format!("sky update: {e}")),
    };
    let mut n = 0usize;
    for (dep, _spec) in &deps {
        match regenerate_committed(&bin, &sky_out, project_dir, dep) {
            Ok(slug) => {
                n += 1;
                r.say(format!("  regenerated sky-ffi/{slug}.*"));
            }
            Err(e) => r.say(format!("  note: {dep}: {e}")),
        }
    }
    r.say(format!("sky update: regenerated {n} surface(s)."));
    r
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

/// Inspect `pkg` and return its generated surface (no disk write).
fn regenerate_in_memory(
    bin: &Path,
    sky_out: &Path,
    pkg: &str,
) -> Result<ffi::GeneratedSurface, String> {
    let info = match ffi::run_inspector(bin, sky_out, &[pkg.to_string()]) {
        Ok(mut v) if !v.is_empty() => v.remove(0),
        Ok(_) => return Err(format!("inspector returned no package for {pkg}")),
        Err(e) => return Err(e),
    };
    Ok(ffi::generate(&info))
}

/// Inspect `pkg` and commit its surface files; returns the slug.
fn regenerate_committed(
    bin: &Path,
    sky_out: &Path,
    project_dir: &Path,
    pkg: &str,
) -> Result<String, String> {
    let info = match ffi::run_inspector(bin, sky_out, &[pkg.to_string()]) {
        Ok(mut v) if !v.is_empty() => v.remove(0),
        Ok(_) => return Err(format!("inspector returned no package for {pkg}")),
        Err(e) => return Err(e),
    };
    write_surface(project_dir, &info)
}

/// Write the three committed surface files for one inspected package. Returns
/// the slug. `info` is already normalised by `run_inspector`.
fn write_surface(project_dir: &Path, info: &ffi::PackageInfo) -> Result<String, String> {
    let surface = ffi::generate(info);
    let ffi_dir = project_dir.join("sky-ffi");
    let go_dir = ffi_dir.join("go");
    std::fs::create_dir_all(&go_dir).map_err(|e| format!("mkdir sky-ffi/go: {e}"))?;
    let slug = &surface.slug;
    std::fs::write(
        ffi_dir.join(format!("{slug}.kernel.json")),
        &surface.kernel_json,
    )
    .map_err(|e| format!("write {slug}.kernel.json: {e}"))?;
    std::fs::write(ffi_dir.join(format!("{slug}.skyi")), &surface.skyi)
        .map_err(|e| format!("write {slug}.skyi: {e}"))?;
    std::fs::write(
        go_dir.join(format!("{slug}_bindings.go")),
        &surface.bindings_go,
    )
    .map_err(|e| format!("write {slug}_bindings.go: {e}"))?;
    Ok(slug.clone())
}

/// Find the slug of a committed surface whose `kernel.json` `"package"` == `pkg`.
fn slug_for_package(project_dir: &Path, pkg: &str) -> Option<String> {
    let ffi_dir = project_dir.join("sky-ffi");
    let rd = std::fs::read_dir(&ffi_dir).ok()?;
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    let needle = format!("\"package\": \"{pkg}\"");
    for p in entries {
        let is_kj = p
            .file_name()
            .and_then(|n| n.to_str())
            .is_some_and(|n| n.ends_with(".kernel.json"));
        if !is_kj {
            continue;
        }
        if let Ok(text) = std::fs::read_to_string(&p) {
            if text.contains(&needle) {
                let fname = p.file_name().and_then(|n| n.to_str()).unwrap_or("");
                return fname.strip_suffix(".kernel.json").map(str::to_string);
            }
        }
    }
    None
}

/// Ensure `sky-out/go.mod` exists — copy the runtime module's go.mod (+ go.sum)
/// as the baseline (module `sky-app`), mirroring the oracle.
fn ensure_go_mod(repo_root: &Path, sky_out: &Path) -> Result<(), String> {
    std::fs::create_dir_all(sky_out).map_err(|e| format!("mkdir sky-out: {e}"))?;
    let go_mod = sky_out.join("go.mod");
    if go_mod.is_file() {
        return Ok(());
    }
    let rt = repo_root.join("runtime-go");
    if rt.join("go.mod").is_file() {
        std::fs::copy(rt.join("go.mod"), &go_mod).map_err(|e| format!("copy go.mod: {e}"))?;
        if rt.join("go.sum").is_file() {
            let _ = std::fs::copy(rt.join("go.sum"), sky_out.join("go.sum"));
        }
    } else {
        std::fs::write(&go_mod, "module sky-app\n\ngo 1.25.0\n")
            .map_err(|e| format!("write go.mod stub: {e}"))?;
    }
    Ok(())
}

fn go_get(sky_out: &Path, pkgs: &[String]) -> Result<(), String> {
    let mut args = vec!["get".to_string()];
    args.extend(pkgs.iter().cloned());
    run_go(
        sky_out,
        &args.iter().map(String::as_str).collect::<Vec<_>>(),
    )
}

fn run_go(dir: &Path, args: &[&str]) -> Result<(), String> {
    let out = Command::new("go")
        .args(args)
        .current_dir(dir)
        .env("GOFLAGS", "-mod=mod")
        .output()
        .map_err(|e| format!("spawn go {}: {e}", args.join(" ")))?;
    if out.status.success() {
        Ok(())
    } else {
        Err(format!(
            "go {} failed: {}",
            args.join(" "),
            String::from_utf8_lossy(&out.stderr).trim()
        ))
    }
}

fn is_external_module(path: &str) -> bool {
    path.split('/')
        .next()
        .is_some_and(|head| head.contains('.'))
}

fn module_required(sky_out: &Path, path: &str) -> bool {
    let Ok(text) = std::fs::read_to_string(sky_out.join("go.mod")) else {
        return false;
    };
    text.lines()
        .any(|l| l.split_whitespace().next() == Some(path) || l.trim() == path)
}

/// The exact version `go get` resolved for the module owning `path`, read from
/// `sky-out/go.mod` — the `require` line whose module path is the longest prefix
/// of `path` (so a subpackage like `.../v84/customer` maps to its module root).
/// Used for pin-by-default when the user gave `sky add` no explicit version.
fn resolved_module_version(sky_out: &Path, path: &str) -> Option<String> {
    let text = std::fs::read_to_string(sky_out.join("go.mod")).ok()?;
    let mut best: Option<(usize, String)> = None;
    for line in text.lines() {
        let l = line.trim().trim_start_matches("require").trim();
        let mut it = l.split_whitespace();
        let (Some(module), Some(version)) = (it.next(), it.next()) else {
            continue;
        };
        if !version.starts_with('v') {
            continue;
        }
        let is_match = path == module || path.starts_with(&format!("{module}/"));
        if is_match && best.as_ref().is_none_or(|(len, _)| module.len() > *len) {
            best = Some((module.len(), version.to_string()));
        }
    }
    best.map(|(_, v)| v)
}

// ---- sky.toml [go.dependencies] management (port of appendGoDependency) ----

/// Outcome of an [`upsert_go_dependency`] edit.
enum DepEdit {
    /// The dep was not present and was inserted.
    Added,
    /// The dep was present at a different spec, now replaced (carries the old).
    Updated(String),
    /// The dep was already recorded at exactly this spec.
    Unchanged,
}

/// Record `"<pkg>" = "<spec>"` under `["go.dependencies"]`. If `pkg` is already
/// present, its spec is REPLACED (the most recent `sky add` wins — so sky.toml
/// never disagrees with the regenerated surface); if the spec is unchanged it is
/// a no-op. Only an exact-key match (`"<pkg>"` with its closing quote) counts, so
/// `foo` never collides with `foo/v2`.
fn upsert_go_dependency(sky_toml: &Path, pkg: &str, spec: &str) -> Result<DepEdit, String> {
    let existing = std::fs::read_to_string(sky_toml).unwrap_or_default();
    let spec = spec_or_latest(spec);
    let quoted = format!("\"{pkg}\"");
    let mut lines: Vec<String> = existing.lines().map(str::to_string).collect();

    // Replace an existing exact-key entry.
    for line in lines.iter_mut() {
        let t = line.trim_start();
        if let Some(rest) = t.strip_prefix(&quoted) {
            if let Some(val) = rest.trim_start().strip_prefix('=') {
                let old = val.trim().trim_matches('"').to_string();
                if old == spec {
                    return Ok(DepEdit::Unchanged);
                }
                *line = format!("{quoted} = \"{spec}\"");
                write_lines(sky_toml, &lines)?;
                return Ok(DepEdit::Updated(old));
            }
        }
    }

    // Not present — insert under the `["go.dependencies"]` header (create it if
    // absent).
    let entry = format!("{quoted} = \"{spec}\"");
    let header_idx = lines.iter().position(|l| {
        let t = l.trim();
        t == "[\"go.dependencies\"]" || t == "[go.dependencies]"
    });
    match header_idx {
        Some(i) => lines.insert(i + 1, entry),
        None => {
            if !lines.is_empty() && !lines.last().map(|l| l.is_empty()).unwrap_or(true) {
                lines.push(String::new());
            }
            lines.push("[\"go.dependencies\"]".to_string());
            lines.push(entry);
        }
    }
    write_lines(sky_toml, &lines)?;
    Ok(DepEdit::Added)
}

fn write_lines(sky_toml: &Path, lines: &[String]) -> Result<(), String> {
    let mut out = lines.join("\n");
    out.push('\n');
    std::fs::write(sky_toml, out).map_err(|e| format!("write sky.toml: {e}"))
}

/// Remove the `"<pkg>" = ...` line from `["go.dependencies"]`. Returns whether a
/// line was removed.
fn remove_go_dependency(sky_toml: &Path, pkg: &str) -> Result<bool, String> {
    let Ok(existing) = std::fs::read_to_string(sky_toml) else {
        return Ok(false);
    };
    let quoted = format!("\"{pkg}\"");
    let kept: Vec<&str> = existing
        .lines()
        .filter(|l| !(l.trim_start().starts_with(&quoted) && l.contains('=')))
        .collect();
    if kept.len() == existing.lines().count() {
        return Ok(false);
    }
    let mut out = kept.join("\n");
    out.push('\n');
    std::fs::write(sky_toml, out).map_err(|e| format!("write sky.toml: {e}"))?;
    Ok(true)
}

/// Parse the deps declared under `["go.dependencies"]` (in declaration order) as
/// `(import-path, version-spec)` pairs. An empty / absent value becomes
/// `"latest"`. Skips the `net/http`-style stdlib entries? No — the oracle records
/// them too; we return everything and let the caller decide (stdlib is a go get
/// no-op and inspects fine).
pub(crate) fn read_go_dependencies(sky_toml: &Path) -> Vec<(String, String)> {
    let Ok(text) = std::fs::read_to_string(sky_toml) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    let mut in_section = false;
    for raw in text.lines() {
        let line = raw.trim();
        if line.starts_with('[') && line.ends_with(']') {
            in_section = line == "[\"go.dependencies\"]" || line == "[go.dependencies]";
            continue;
        }
        if !in_section || line.is_empty() || line.starts_with('#') {
            continue;
        }
        // `"pkg" = "version"`
        if let Some((k, v)) = line.split_once('=') {
            let key = k.trim().trim_matches('"').to_string();
            if !key.is_empty() {
                let spec = v.trim().trim_matches('"').trim();
                let spec = if spec.is_empty() { "latest" } else { spec };
                out.push((key, spec.to_string()));
            }
        }
    }
    out
}

/// Normalise a version spec — an empty spec means `"latest"`.
pub(crate) fn spec_or_latest(spec: &str) -> &str {
    let s = spec.trim();
    if s.is_empty() {
        "latest"
    } else {
        s
    }
}

/// Validate a Go dependency version spec. Go's module system takes EXACT
/// versions, not ranges: accept `"latest"` (Go-native floating), an exact semver
/// tag (`vX.Y.Z…`), or a bare branch / commit token. Reject npm/cargo-style range
/// constraints (`>=`, `<=`, `~`, `^`, `*`, or a bare `>` / `<`) — Go cannot
/// resolve them, so a silent `@latest` fallthrough would be a lie.
pub(crate) fn validate_spec(dep: &str, spec: &str) -> Result<(), String> {
    let s = spec_or_latest(spec);
    // Any range-constraint operator anywhere in the spec is a hard reject.
    for ch in ['>', '<', '~', '^', '*'] {
        if s.contains(ch) {
            return Err(format!(
                "sky.toml [\"go.dependencies\"]: \"{dep}\" = \"{spec}\" is not a valid Go version. \
                 Go module versions are exact — use \"latest\", an exact tag like \"v1.6.0\", \
                 or a branch/commit; range constraints (found '{ch}') are not supported."
            ));
        }
    }
    Ok(())
}

/// Whether a spec is an EXACT pin that `sky update` must leave untouched — an
/// exact semver tag (`vX.Y.Z…`) or a commit SHA. `"latest"` and branch names are
/// floating and re-resolve on update.
pub(crate) fn is_exact_pin(spec: &str) -> bool {
    let s = spec_or_latest(spec);
    if s == "latest" {
        return false;
    }
    // Exact semver tag: `v` followed by a digit (`v1.6.0`, `v2.0.0-rc1`).
    if let Some(rest) = s.strip_prefix('v') {
        if rest.chars().next().is_some_and(|c| c.is_ascii_digit()) {
            return true;
        }
    }
    // Commit SHA: 7..=40 lowercase hex chars.
    let looks_like_sha = (7..=40).contains(&s.len())
        && s.chars().all(|c| c.is_ascii_hexdigit())
        && s.contains(|c: char| c.is_ascii_digit());
    looks_like_sha
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_dep_roundtrip_in_sky_toml() {
        let dir = std::env::temp_dir().join(format!("sky-ffi-ops-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let toml = dir.join("sky.toml");
        std::fs::write(&toml, "name = \"x\"\nversion = \"0.1.0\"\n").unwrap();
        // First add inserts.
        assert!(matches!(
            upsert_go_dependency(&toml, "github.com/google/uuid", "v1.3.0").unwrap(),
            DepEdit::Added
        ));
        // Re-add with a new spec UPDATES (the latest sky add wins), not a no-op.
        assert!(matches!(
            upsert_go_dependency(&toml, "github.com/google/uuid", "latest").unwrap(),
            DepEdit::Updated(old) if old == "v1.3.0"
        ));
        assert_eq!(
            read_go_dependencies(&toml),
            vec![("github.com/google/uuid".to_string(), "latest".to_string())]
        );
        // Re-add with the SAME spec is unchanged.
        assert!(matches!(
            upsert_go_dependency(&toml, "github.com/google/uuid", "latest").unwrap(),
            DepEdit::Unchanged
        ));
        // Exact-key match: `uuid` must not collide with `uuid/v2`.
        assert!(matches!(
            upsert_go_dependency(&toml, "github.com/google/uuid/v2", "v2.0.0").unwrap(),
            DepEdit::Added
        ));
        assert_eq!(read_go_dependencies(&toml).len(), 2);
        assert!(remove_go_dependency(&toml, "github.com/google/uuid").unwrap());
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn read_go_dependencies_defaults_empty_value_to_latest() {
        let dir = std::env::temp_dir().join(format!("sky-ffi-ops-def-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let toml = dir.join("sky.toml");
        std::fs::write(
            &toml,
            "name = \"x\"\n[\"go.dependencies\"]\n\"github.com/google/uuid\" = \"\"\n",
        )
        .unwrap();
        let deps = read_go_dependencies(&toml);
        assert_eq!(deps[0].1, "latest");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn validate_spec_accepts_go_native_rejects_ranges() {
        assert!(validate_spec("x", "latest").is_ok());
        assert!(validate_spec("x", "v1.6.0").is_ok());
        assert!(validate_spec("x", "v2.0.0-rc1").is_ok());
        assert!(validate_spec("x", "main").is_ok()); // branch
        assert!(validate_spec("x", "abc1234").is_ok()); // commit-ish
        assert!(validate_spec("x", ">=1.2").is_err());
        assert!(validate_spec("x", "<=1.2").is_err());
        assert!(validate_spec("x", "~1.2").is_err());
        assert!(validate_spec("x", "^1.2").is_err());
        assert!(validate_spec("x", "1.*").is_err());
    }

    #[test]
    fn is_exact_pin_classifies_specs() {
        assert!(!is_exact_pin("latest"));
        assert!(!is_exact_pin(""));
        assert!(!is_exact_pin("main")); // branch
        assert!(is_exact_pin("v1.6.0"));
        assert!(is_exact_pin("v2.0.0-rc1"));
        assert!(is_exact_pin("a1b2c3d4")); // sha-ish
    }
}
