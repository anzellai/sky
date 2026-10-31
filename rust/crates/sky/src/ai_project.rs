//! The project-mode `sky ai` verbs: build the project's AI pipeline
//! (`src/Ai.sky`, `Std.Ai.Pipeline`) and run it.
//!
//! The CLI holds no logic of its own here either. It synthesises an entry
//! module whose `main` is `Std.Ai.Pipeline.commandLine Ai.pipeline`, builds it
//! with the project's own modules (only `Ai` and what it imports are checked,
//! [`project::AppScope::EntryClosure`]), and runs the binary with the verb's
//! arguments in the project directory, where the app's relative paths (its
//! SQLite file, `.sky/ai/registry`) resolve.
//!
//! The binary is cached under `<project>/.sky/ai/cli/<key>/`, where the key is
//! the SHA-256 of the Sky version, `sky.toml` and every source file the build
//! reads (the source root, `.skydeps/`, `sky-ffi/`). An unchanged project runs
//! the cached binary at once; a change builds a new one into
//! `<key>.tmp-<pid>`, renames it into place and removes the older keys, so a
//! build that dies leaves no half-written cache entry.

use std::path::{Path, PathBuf};

/// Where the pipeline binaries are cached, under the project.
pub const CACHE_DIR: &str = ".sky/ai/cli";

/// The cache key: SHA-256 over the version slug, `sky.toml` and every build
/// input file (sorted by path, each with its path and length so two layouts
/// with the same bytes differ).
pub fn cache_key(project_dir: &Path, version_slug: &str) -> String {
    let mut files = Vec::new();
    let root = project_dir.join(project::configured_source_root(project_dir));
    collect_inputs(&root, &mut files);
    for extra in [".skydeps", "sky-ffi"] {
        collect_inputs(&project_dir.join(extra), &mut files);
    }
    files.sort();
    files.dedup();
    let mut h = project::sha256::Sha256::new();
    h.update(b"sky-ai-pipeline-v1\n");
    h.update(version_slug.as_bytes());
    h.update(b"\n");
    let toml = std::fs::read(project_dir.join("sky.toml")).unwrap_or_default();
    h.update(format!("sky.toml {}\n", toml.len()).as_bytes());
    h.update(&toml);
    for f in files {
        let rel = f.strip_prefix(project_dir).unwrap_or(&f);
        let data = std::fs::read(&f).unwrap_or_default();
        h.update(format!("\n{} {}\n", rel.display(), data.len()).as_bytes());
        h.update(&data);
    }
    h.finish().iter().map(|b| format!("{b:02x}")).collect()
}

/// The build inputs under a directory: every file except hidden ones and
/// build output (`sky-out`, `sky-out-rust`, `.skycache`).
fn collect_inputs(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in rd.filter_map(Result::ok) {
        let path = entry.path();
        let name = entry.file_name().to_string_lossy().to_string();
        if name.starts_with('.') || name == "sky-out" || name == "sky-out-rust" {
            continue;
        }
        if path.is_dir() {
            collect_inputs(&path, out);
        } else {
            out.push(path);
        }
    }
}

/// Build the pipeline binary for a project, or reuse the cached one. Returns
/// the binary's path. Progress goes to stderr: a verb's stdout may be one
/// JSON value (`--format json`).
pub fn ensure_built(
    repo_root: &Path,
    project_dir: &Path,
    version_slug: &str,
) -> Result<PathBuf, String> {
    let key = cache_key(project_dir, version_slug);
    let cache = project_dir.join(CACHE_DIR);
    let bin_name = project::configured_bin_name(project_dir);
    let ready = cache.join(&key).join("sky-out").join(&bin_name);
    if ready.is_file() {
        return Ok(ready);
    }
    std::fs::create_dir_all(&cache)
        .map_err(|e| format!("sky ai: cannot create {}: {e}", cache.display()))?;
    let tmp = cache.join(format!("{key}.tmp-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&tmp);
    // The synthesised entry lives OUTSIDE the project tree, so a project whose
    // source root is the project directory itself never loads it as its own.
    let scratch = std::env::temp_dir().join(format!(
        "sky-ai-entry-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or(0)
    ));
    std::fs::create_dir_all(&scratch)
        .map_err(|e| format!("sky ai: cannot create {}: {e}", scratch.display()))?;
    let entry_file = scratch.join(format!("{}.sky", project::ai_entry::AI_ENTRY_MODULE));
    let (config_module, config_warning) = project::ai_entry::config_source(project_dir);
    if let Some(w) = config_warning {
        eprintln!("warning: {w}");
    }
    std::fs::write(
        &entry_file,
        project::ai_entry::entry_source_with(config_module.as_deref()),
    )
    .map_err(|e| format!("sky ai: cannot write {}: {e}", entry_file.display()))?;
    eprintln!("sky ai: building the project's AI pipeline (src/Ai.sky)...");
    let out_dir = tmp.join("sky-out");
    let opts = project::BuildOptions {
        repo_root: repo_root.to_path_buf(),
        example_dir: project_dir.to_path_buf(),
        out_dir_name: "sky-out".to_string(),
        out_dir_abs: Some(out_dir.clone()),
        run: false,
        stdin: None,
        entry_module: None,
        progress: false,
        embed_bundle: None,
        wasm: false,
    };
    let report = project::build_project_scoped(
        &opts,
        std::slice::from_ref(&scratch),
        Some(project::ai_entry::AI_ENTRY_MODULE),
        project::AppScope::EntryClosure,
    );
    let _ = std::fs::remove_dir_all(&scratch);
    for w in &report.warnings {
        eprintln!("warning: {w}");
    }
    if !report.emitted {
        let _ = std::fs::remove_dir_all(&tmp);
        return Err(format!(
            "sky ai: the AI pipeline (src/Ai.sky) does not build:\n{}",
            report.note
        ));
    }
    if !report.go_build_ok {
        let _ = std::fs::remove_dir_all(&tmp);
        return Err(format!(
            "sky ai: go build failed for the AI pipeline:\n{}",
            report.go_build_stderr
        ));
    }
    let dest = cache.join(&key);
    let _ = std::fs::remove_dir_all(&dest);
    std::fs::rename(&tmp, &dest).map_err(|e| {
        format!(
            "sky ai: cannot move the pipeline build into {}: {e}",
            dest.display()
        )
    })?;
    prune(&cache, &key);
    let bin = dest.join("sky-out").join(&bin_name);
    if !bin.is_file() {
        return Err(format!(
            "sky ai: the build reported success but {} is missing",
            bin.display()
        ));
    }
    Ok(bin)
}

/// Remove every cache entry but `keep`, and temporary builds older than an
/// hour (a concurrent build's live temporary is younger).
fn prune(cache: &Path, keep: &str) {
    let Ok(rd) = std::fs::read_dir(cache) else {
        return;
    };
    for entry in rd.filter_map(Result::ok) {
        let name = entry.file_name().to_string_lossy().to_string();
        if name == keep {
            continue;
        }
        let stale_tmp = name.contains(".tmp-")
            && entry
                .metadata()
                .and_then(|m| m.modified())
                .ok()
                .and_then(|t| t.elapsed().ok())
                .is_some_and(|age| age > std::time::Duration::from_secs(3600));
        if !name.contains(".tmp-") || stale_tmp {
            let _ = std::fs::remove_dir_all(entry.path());
        }
    }
}

/// What `sky ai data freeze <arg>` means: a dataset of the pipeline when the
/// argument is a plain name (no `/`, no `.`) and no file of that name exists;
/// otherwise a JSONL file for the bundled program.
pub fn freeze_names_a_dataset(arg: &str, cwd: &Path) -> bool {
    !arg.contains('/') && !arg.contains('.') && !cwd.join(arg).is_file()
}

/// The pipeline's slot names, from `<bin> members --format json`.
pub fn pipeline_slots(bin: &Path, project_dir: &Path) -> Result<Vec<String>, String> {
    let out = std::process::Command::new(bin)
        .args(["members", "--format", "json"])
        .current_dir(project_dir)
        .stdin(std::process::Stdio::null())
        .output()
        .map_err(|e| format!("sky ai: could not run the pipeline: {e}"))?;
    if !out.status.success() {
        return Err(format!(
            "sky ai: the pipeline could not list its members:\n{}",
            String::from_utf8_lossy(&out.stderr)
        ));
    }
    let v: serde_json::Value = serde_json::from_slice(&out.stdout)
        .map_err(|e| format!("sky ai: the pipeline's members are not JSON: {e}"))?;
    Ok(v["slots"]
        .as_array()
        .map(|a| {
            a.iter()
                .filter_map(|s| s.as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn project() -> PathBuf {
        static N: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let dir = std::env::temp_dir().join(format!(
            "sky-ai-project-{}-{}-{}",
            (u64::from(std::process::id()) << 32) | {
                static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
                SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
            },
            N.fetch_add(1, std::sync::atomic::Ordering::SeqCst),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos())
                .unwrap_or(0)
        ));
        std::fs::create_dir_all(dir.join("src")).unwrap();
        std::fs::write(
            dir.join("sky.toml"),
            "name = \"x\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
        )
        .unwrap();
        std::fs::write(dir.join("src/Ai.sky"), "module Ai exposing (pipeline)\n").unwrap();
        dir
    }

    /// The key moves with a source edit, a new file, sky.toml and the
    /// version, and not with build output or the cache itself.
    #[test]
    fn the_cache_key_follows_the_inputs() {
        let dir = project();
        let k0 = cache_key(&dir, "v1");
        assert_eq!(k0, cache_key(&dir, "v1"), "stable");
        assert_ne!(k0, cache_key(&dir, "v2"), "the version");
        std::fs::create_dir_all(dir.join("src/sky-out")).unwrap();
        std::fs::write(dir.join("src/sky-out/app"), "binary").unwrap();
        std::fs::create_dir_all(dir.join(".sky/ai/cli/old")).unwrap();
        std::fs::write(dir.join(".sky/ai/cli/old/x"), "x").unwrap();
        assert_eq!(k0, cache_key(&dir, "v1"), "build output and the cache");
        std::fs::write(
            dir.join("src/Ai.sky"),
            "module Ai exposing (pipeline)\n\n-- x\n",
        )
        .unwrap();
        let k1 = cache_key(&dir, "v1");
        assert_ne!(k0, k1, "a source edit");
        std::fs::write(dir.join("src/Data.sky"), "module Data exposing (x)\n").unwrap();
        let k2 = cache_key(&dir, "v1");
        assert_ne!(k1, k2, "a new module");
        std::fs::write(dir.join("sky.toml"), "name = \"y\"\n").unwrap();
        assert_ne!(k2, cache_key(&dir, "v1"), "sky.toml");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn prune_keeps_the_current_key_and_a_fresh_temporary() {
        let dir = project();
        let cache = dir.join(CACHE_DIR);
        for d in ["aaa", "bbb", "bbb.tmp-1"] {
            std::fs::create_dir_all(cache.join(d)).unwrap();
        }
        prune(&cache, "bbb");
        assert!(!cache.join("aaa").exists());
        assert!(cache.join("bbb").exists());
        assert!(cache.join("bbb.tmp-1").exists(), "a live build's temporary");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn freeze_tells_a_dataset_name_from_a_file() {
        let dir = project();
        assert!(freeze_names_a_dataset("support", &dir));
        assert!(!freeze_names_a_dataset("data/support.jsonl", &dir));
        assert!(!freeze_names_a_dataset("support.jsonl", &dir));
        std::fs::write(dir.join("tickets"), "{}\n").unwrap();
        assert!(!freeze_names_a_dataset("tickets", &dir), "an existing file");
        let _ = std::fs::remove_dir_all(&dir);
    }
}
