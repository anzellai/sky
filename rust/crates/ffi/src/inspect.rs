//! Deterministic driver for the Go `sky-ffi-inspect` introspector (doc 09
//! §C.2/§C.3/§C.4). This is the out-of-band provenance tool that turns a Go
//! package into typed FFI data — it runs ONLY at `sky add`/`install`/`update`
//! time, never mid-build (doc 09 §C.1 rule 2).
//!
//! Three L4 wins live here:
//!   * **Determinism (B.2).** The Go tool emits `implements` slices and the
//!     `functions` list in Go map-iteration / method-set order, which varies
//!     run-to-run. [`normalize`] sorts every collection before it reaches the
//!     surface — the Rust-side analogue of the workspace "no HashMap iteration
//!     reaches output" rule. Empirically byte-stable across 5 runs post-sort.
//!   * **Platform pinning (B.3).** [`run_inspector`] pins `GOOS=linux
//!     GOARCH=amd64` (doc 09 §C.4) so a macOS dev and linux CI inspect the same
//!     surface for the common pure-Go SDK case. Verified: normalized uuid is
//!     byte-identical host vs linux.
//!   * **Tool provenance (§C.3).** [`ensure_inspector`] materialises the
//!     `tools/sky-ffi-inspect/` source tree into a content-hashed XDG-cache dir
//!     and `go build`s it once, mirroring `EmbeddedInspector.ensureInspector`.

use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use std::process::Command;

// ---------------------------------------------------------------------------
// Inspector JSON model — mirrors `tools/sky-ffi-inspect/main.go`'s
// PackageInfo / Function / Param structs exactly (field names + json tags).
// ---------------------------------------------------------------------------

/// One parameter or result slot of a Go function, as the inspector reports it.
#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
pub struct Param {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    /// Canonical Go type string (`string`, `*pkg.Foo`, `[]byte`, `func(...)`).
    /// Drives Go-wrapper codegen so derived types stay distinct from basics.
    #[serde(rename = "type")]
    pub ty: String,
    /// Sky-side surface form; differs from `ty` only for basic-type aliases
    /// (`type Direction int` → `int`). Empty when it equals `ty`.
    #[serde(rename = "skyType", default, skip_serializing_if = "String::is_empty")]
    pub sky_type: String,
    /// Opaque-distinctness marker: `Name@importPath` when the underlying Go
    /// type is an opaque named struct/interface. Empty otherwise.
    #[serde(
        rename = "skyTypeQualified",
        default,
        skip_serializing_if = "String::is_empty"
    )]
    pub sky_type_qualified: String,
}

/// One exported Go function / method / synthetic accessor.
#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
pub struct Function {
    pub name: String,
    #[serde(default)]
    pub params: Vec<Param>,
    #[serde(default)]
    pub results: Vec<Param>,
    #[serde(default)]
    pub variadic: bool,
    #[serde(default)]
    pub effect: String,
    #[serde(default)]
    pub exported: bool,
    /// Go receiver type name for method wrappers (`Router`); empty for frees.
    #[serde(rename = "recvType", default, skip_serializing_if = "String::is_empty")]
    pub recv_type: String,
    #[serde(
        rename = "methodName",
        default,
        skip_serializing_if = "String::is_empty"
    )]
    pub method_name: String,
    #[serde(rename = "isField", default, skip_serializing_if = "is_false")]
    pub is_field: bool,
    #[serde(rename = "isFieldSet", default, skip_serializing_if = "is_false")]
    pub is_field_set: bool,
    #[serde(rename = "isPkgVar", default, skip_serializing_if = "is_false")]
    pub is_pkg_var: bool,
}

fn is_false(b: &bool) -> bool {
    !*b
}

/// The inspector's per-package report.
#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
pub struct PackageInfo {
    pub pkg: String,
    pub name: String,
    #[serde(default)]
    pub functions: Vec<Function>,
    /// `Name@pkg` → the qualified interface names it satisfies.
    #[serde(default, skip_serializing_if = "std::collections::BTreeMap::is_empty")]
    pub implements: std::collections::BTreeMap<String, Vec<String>>,
    /// Go import path → canonical safe-identifier alias.
    #[serde(
        rename = "pkgAlias",
        default,
        skip_serializing_if = "std::collections::BTreeMap::is_empty"
    )]
    pub pkg_alias: std::collections::BTreeMap<String, String>,
    #[serde(default)]
    pub errors: Option<Vec<String>>,
}

// ---------------------------------------------------------------------------
// Normalisation (doc 09 §C.2 — the L4 B.2 close).
// ---------------------------------------------------------------------------

/// Canonicalise an inspector report so the emitted surface is a pure function
/// of the package's typed shape — never of Go map-iteration order.
///
/// * `functions` sorted by `(name, recv_type, method_name)` so field/method
///   overloads that share a lowercased name stay in a stable order.
/// * each `implements` value slice `sort`ed + deduped (the confirmed
///   run-to-run leak — see module docs).
/// * `implements` / `pkg_alias` keys are already sorted (`BTreeMap`).
///
/// `params` / `results` order is semantically load-bearing (it IS the Go
/// signature) and Go returns `scope.Names()` sorted, so those are left intact.
pub fn normalize(info: &mut PackageInfo) {
    info.functions.sort_by(|a, b| {
        (&a.name, &a.recv_type, &a.method_name, &a.is_field_set).cmp(&(
            &b.name,
            &b.recv_type,
            &b.method_name,
            &b.is_field_set,
        ))
    });
    for vs in info.implements.values_mut() {
        vs.sort();
        vs.dedup();
    }
}

/// Parse inspector stdout (single-object OR one element of the multi array).
pub fn parse_one(json: &str) -> Result<PackageInfo, String> {
    serde_json::from_str::<PackageInfo>(json).map_err(|e| format!("inspector JSON parse: {e}"))
}

// ---------------------------------------------------------------------------
// The pinned target (doc 09 §C.4).
// ---------------------------------------------------------------------------

/// The canonical inspection target: the deploy + CI platform. Committing the
/// surface generated for this one target is what makes a macOS dev and a linux
/// CI read identical bytes (doc 09 §C.4 "Default — committed, platform-pinned").
pub const PIN_GOOS: &str = "linux";
pub const PIN_GOARCH: &str = "amd64";

/// Run the inspector over `pkgs` with the pinned target, resolving against the
/// `go.mod` in `work_dir` (the project's `sky-out/`, exactly as the oracle's
/// `cd sky-out && <bin> <pkg>`). Returns one normalised [`PackageInfo`] per
/// requested package, matched back by import path (multi-mode) or the single
/// object (single-mode).
pub fn run_inspector(
    bin: &Path,
    work_dir: &Path,
    pkgs: &[String],
) -> Result<Vec<PackageInfo>, String> {
    run_inspector_reporting(bin, work_dir, pkgs).map(|(infos, _)| infos)
}

/// How a package's surface was inspected. Part of the surface's provenance: a
/// host-inspected surface is NOT portable and must not be compared byte-for-byte
/// against one generated on another machine.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum InspectTarget {
    /// The pinned `linux/amd64`, `CGO_ENABLED=0` target — reproducible anywhere.
    Pinned,
    /// The host's own `GOOS`/`GOARCH` with cgo ENABLED. Used only for packages
    /// that cannot be type-checked under the pinned target at all.
    HostCgo,
}

/// Like [`run_inspector`], but also reports a note when the **host + cgo
/// fallback** was used, so the caller can warn and record the provenance.
///
/// # Why a fallback exists
///
/// The pinned target (`linux/amd64`, `CGO_ENABLED=0`) is what makes a macOS dev
/// and a Linux CI read identical surface bytes, and it stays the default for
/// every package. But it cannot type-check a package that genuinely REQUIRES
/// cgo: Fyne reaches glfw → OpenGL → the platform's native windowing, none of
/// which exists under `CGO_ENABLED=0`. Such a package could not be inspected on
/// ANY machine, so `sky install` could not generate its surface, and
/// `11-fyne-stopwatch` was verified on no platform at all — Linux CI skipped it
/// and macOS could not build it (GitHub discussion #50).
///
/// So: try the pinned target first, always. Only if that fails outright, retry
/// on the host's own target with cgo enabled. Normalisation is unchanged for
/// everything else, and a package that succeeds under the pin never touches this
/// path — the reproducibility guarantee is preserved exactly where it can hold,
/// and traded away only where the alternative is no surface at all.
///
/// The returned note names the target used. A surface produced this way is
/// host-specific by construction: it describes the platform it was generated on.
pub fn run_inspector_reporting(
    bin: &Path,
    work_dir: &Path,
    pkgs: &[String],
) -> Result<(Vec<PackageInfo>, Option<String>), String> {
    if pkgs.is_empty() {
        return Ok((Vec::new(), None));
    }
    match run_inspector_on(bin, work_dir, pkgs, InspectTarget::Pinned) {
        Ok(infos) => Ok((infos, None)),
        Err(pinned_err) => {
            // ANY pinned-target failure earns one retry on the host. The failure
            // modes are not reliably distinguishable from the message: a
            // cgo-requiring package can present as "build constraints exclude all
            // Go files", as an undefined C symbol, OR — as Fyne actually does —
            // as a plain type error deep inside its own driver, because
            // CGO_ENABLED=0 selects a stub file set whose type no longer
            // satisfies the interface ("gLDriver does not implement fyne.Driver
            // (missing method DoubleTapDelay)"). A keyword whitelist over that
            // surface would be permanently incomplete.
            //
            // Retrying broadly is safe because the retry only ever turns a
            // FAILURE into a success, never changes a successful pinned result,
            // and always reports its provenance. A genuinely broken or misspelt
            // package fails both ways and gets both errors.
            match run_inspector_on(bin, work_dir, pkgs, InspectTarget::HostCgo) {
                Ok(infos) => Ok((
                    infos,
                    Some(format!(
                        "FFI surface for {} was generated on the HOST target \
                         ({}/{}, cgo enabled) because it cannot be type-checked \
                         under the pinned {PIN_GOOS}/{PIN_GOARCH} CGO_ENABLED=0 \
                         target. This surface describes THIS platform and is not \
                         portable — regenerate it (`sky install`) on any machine \
                         whose surface you need, and do not expect it to match \
                         another platform's byte-for-byte.",
                        pkgs.join(", "),
                        std::env::consts::OS,
                        std::env::consts::ARCH,
                    )),
                )),
                // Report the PINNED error: it is the one describing the default
                // path, and the fallback failing too means neither works.
                Err(host_err) => Err(format!(
                    "{pinned_err}\n\nhost+cgo fallback also failed:\n{host_err}"
                )),
            }
        }
    }
}

/// One inspector invocation at a given target.
fn run_inspector_on(
    bin: &Path,
    work_dir: &Path,
    pkgs: &[String],
    target: InspectTarget,
) -> Result<Vec<PackageInfo>, String> {
    let mut cmd = Command::new(bin);
    cmd.args(pkgs).current_dir(work_dir);
    match target {
        InspectTarget::Pinned => {
            cmd.env("GOOS", PIN_GOOS)
                .env("GOARCH", PIN_GOARCH)
                // CGO off: cross-GOOS type-checking of pure-Go SDKs must not try
                // to compile cgo for the host (doc 09 §C.4 mitigation).
                .env("CGO_ENABLED", "0");
        }
        InspectTarget::HostCgo => {
            // Inherit the host's GOOS/GOARCH by REMOVING any pin the parent
            // process may carry, and turn cgo on so the C-backed files are in
            // the build at all.
            cmd.env_remove("GOOS")
                .env_remove("GOARCH")
                .env("CGO_ENABLED", "1");
        }
    }
    // ETXTBSY (errno 26 on Linux and macOS alike): the `sky-ffi-inspect` binary
    // can still be open for writing by a concurrent linker at the instant we
    // execve it — seen under `cargo test --workspace` load, and equally possible
    // for a `sky install` that just rebuilt the inspector. That is a transient
    // race, not a real failure, so retry with a short backoff before surfacing
    // it. Any other spawn error fails immediately.
    let out = {
        let mut attempt: u32 = 0;
        loop {
            match cmd.output() {
                Ok(o) => break o,
                Err(e) if e.raw_os_error() == Some(26) && attempt < 10 => {
                    attempt += 1;
                    std::thread::sleep(std::time::Duration::from_millis(
                        u64::from(20 * attempt),
                    ));
                }
                Err(e) => return Err(format!("spawn sky-ffi-inspect: {e}")),
            }
        }
    };
    let stdout = String::from_utf8_lossy(&out.stdout);
    if stdout.trim().is_empty() {
        let err = String::from_utf8_lossy(&out.stderr);
        return Err(format!("inspector produced no output. stderr:\n{err}"));
    }
    // Single-package mode emits a bare object; multi emits an array.
    let mut infos: Vec<PackageInfo> = if pkgs.len() == 1 {
        vec![parse_one(&stdout)?]
    } else {
        serde_json::from_str::<Vec<PackageInfo>>(&stdout)
            .map_err(|e| format!("inspector JSON array parse: {e}"))?
    };
    // Surface any inspector-reported per-package error.
    for info in &infos {
        if let Some(errs) = &info.errors {
            if !errs.is_empty() {
                return Err(format!(
                    "inspector error for {}: {}",
                    if info.pkg.is_empty() {
                        "<pkg>"
                    } else {
                        &info.pkg
                    },
                    errs.join("; ")
                ));
            }
        }
    }
    for info in &mut infos {
        normalize(info);
    }
    Ok(infos)
}

// ---------------------------------------------------------------------------
// ensure_inspector (doc 09 §C.3) — materialise + `go build` the tool into a
// content-hashed XDG cache, mirroring EmbeddedInspector.ensureInspector.
// ---------------------------------------------------------------------------

/// Materialise the `tools/sky-ffi-inspect/` source tree from `repo_root` into a
/// content-hashed cache dir under `$XDG_CACHE_HOME/sky/tools/` and `go build`
/// it, returning the compiled binary path. Reuses the cached binary on a hash
/// hit (O(stat)); a source change flips the hash and rebuilds.
///
/// The content hash keys the cache so the tool's provenance is pinned: the same
/// inspector source always yields the same cache dir, and `sky upgrade`-style
/// source changes auto-invalidate. (Packaging note: a shipped standalone binary
/// would embed the tree via `include_dir!` per doc 09 §E; in the repo-rooted
/// bring-up we read it from the source tree, which is always present.)
pub fn ensure_inspector(repo_root: &Path) -> Result<PathBuf, String> {
    let src_dir = repo_root.join("tools").join("sky-ffi-inspect");
    if !src_dir.is_dir() {
        return Err(format!(
            "sky-ffi-inspect source tree not found at {}",
            src_dir.display()
        ));
    }
    let files = collect_tool_sources(&src_dir)?;
    let hash = content_hash(&files);
    let cache_root = xdg_cache_sky()
        .join("tools")
        .join(format!("sky-ffi-inspect-{hash}"));
    let bin = cache_root.join(if cfg!(windows) {
        "sky-ffi-inspect.exe"
    } else {
        "sky-ffi-inspect"
    });
    if bin.is_file() {
        return Ok(bin);
    }
    build_inspector(&src_dir, &files, &cache_root, &bin)
}

/// Collect `(relative-path, bytes)` for every file in the tool tree, sorted by
/// path so the hash is enumeration-order independent (mirrors
/// EmbeddedInspector's `sortOn fst`). The prebuilt `sky-ffi-inspect` binary in
/// the tree is skipped — it is an output, not a source.
fn collect_tool_sources(src_dir: &Path) -> Result<Vec<(String, Vec<u8>)>, String> {
    let mut out = Vec::new();
    let mut stack = vec![src_dir.to_path_buf()];
    while let Some(dir) = stack.pop() {
        let rd = std::fs::read_dir(&dir).map_err(|e| format!("read {}: {e}", dir.display()))?;
        for entry in rd.flatten() {
            let path = entry.path();
            if path.is_dir() {
                stack.push(path);
                continue;
            }
            let name = path.file_name().and_then(|n| n.to_str()).unwrap_or("");
            // Skip the committed prebuilt binary + OS junk — sources only.
            if name == "sky-ffi-inspect" || name == "sky-ffi-inspect.exe" || name == ".DS_Store" {
                continue;
            }
            let rel = path
                .strip_prefix(src_dir)
                .unwrap_or(&path)
                .to_string_lossy()
                .into_owned();
            let bytes =
                std::fs::read(&path).map_err(|e| format!("read {}: {e}", path.display()))?;
            out.push((rel, bytes));
        }
    }
    out.sort_by(|a, b| a.0.cmp(&b.0));
    Ok(out)
}

/// Stable content hash (FNV-1a, 64-bit) over the sorted `(path, bytes)` tree.
/// Std-only (no crypto dep) — the cache key only needs to change when the tool
/// source changes, and be reproducible across runs, which FNV-1a satisfies.
fn content_hash(files: &[(String, Vec<u8>)]) -> String {
    const OFFSET: u64 = 0xcbf2_9ce4_8422_2325;
    const PRIME: u64 = 0x0000_0100_0000_01b3;
    let mut h = OFFSET;
    let mut mix = |bytes: &[u8]| {
        for &b in bytes {
            h ^= b as u64;
            h = h.wrapping_mul(PRIME);
        }
    };
    for (path, bytes) in files {
        mix(path.as_bytes());
        mix(&[0]);
        mix(bytes);
        mix(&[0]);
    }
    format!("{h:016x}")
}

pub(crate) fn xdg_cache_sky() -> PathBuf {
    // Explicit override, first — the escape hatch for locked-down environments.
    if let Ok(d) = std::env::var("SKY_CACHE_DIR") {
        if !d.is_empty() {
            return PathBuf::from(d).join("sky");
        }
    }
    // Try the standard locations, but only accept one we can actually WRITE to.
    // An unprivileged environment (container, sandbox, CI, nix build) often has
    // HOME set to a read-only or nonexistent path; the old code committed to
    // `$HOME/.cache/sky` regardless and then failed at extraction time with a
    // permission error (#7). Probe each candidate and fall through to $TMPDIR,
    // which is writable virtually everywhere.
    let mut candidates: Vec<PathBuf> = Vec::new();
    if let Ok(x) = std::env::var("XDG_CACHE_HOME") {
        if !x.is_empty() {
            candidates.push(PathBuf::from(x).join("sky"));
        }
    }
    if let Ok(home) = std::env::var("HOME") {
        if !home.is_empty() {
            candidates.push(PathBuf::from(home).join(".cache").join("sky"));
        }
    }
    for c in &candidates {
        if is_writable_dir(c) {
            return c.clone();
        }
    }
    std::env::temp_dir().join("sky-cache")
}

/// Go env overrides (`GOCACHE`, `GOPATH`) to apply to `go build` when `$HOME`
/// isn't writable — Go's build cache defaults under `$HOME` (`~/.cache/go-build`
/// / `~/Library/Caches/go-build`) and its module cache under `$HOME/go`, so an
/// unprivileged environment fails `go build` even after the Sky asset cache falls
/// back (#7). Routes both to the (probed-writable) Sky cache base. Empty when HOME
/// is writable or the user already set these — normal setups are untouched.
pub fn go_env_for_constrained_home() -> Vec<(String, String)> {
    let home_writable = std::env::var("HOME")
        .ok()
        .filter(|h| !h.is_empty())
        .map(|h| is_writable_dir(&PathBuf::from(h).join(".cache")))
        .unwrap_or(false);
    if home_writable {
        return Vec::new();
    }
    let base = xdg_cache_sky();
    let mut out = Vec::new();
    // Treat an EMPTY value as unset — an empty GOCACHE makes `go` fall back to its
    // HOME default, which is exactly the unwritable path we're routing around.
    if env_unset_or_empty("GOCACHE") {
        out.push((
            "GOCACHE".to_string(),
            base.join("go-build").display().to_string(),
        ));
    }
    if env_unset_or_empty("GOPATH") && env_unset_or_empty("GOMODCACHE") {
        out.push(("GOPATH".to_string(), base.join("go").display().to_string()));
    }
    out
}

fn env_unset_or_empty(key: &str) -> bool {
    std::env::var(key).map(|v| v.is_empty()).unwrap_or(true)
}

/// True iff `dir` can be created and written to — a create + probe-file write.
fn is_writable_dir(dir: &Path) -> bool {
    if std::fs::create_dir_all(dir).is_err() {
        return false;
    }
    let probe = dir.join(".sky-write-probe");
    match std::fs::write(&probe, b"") {
        Ok(()) => {
            let _ = std::fs::remove_file(&probe);
            true
        }
        Err(_) => false,
    }
}

fn build_inspector(
    src_dir: &Path,
    files: &[(String, Vec<u8>)],
    cache_root: &Path,
    bin: &Path,
) -> Result<PathBuf, String> {
    std::fs::create_dir_all(cache_root)
        .map_err(|e| format!("mkdir {}: {e}", cache_root.display()))?;
    for (rel, bytes) in files {
        let dst = cache_root.join(rel);
        if let Some(parent) = dst.parent() {
            std::fs::create_dir_all(parent)
                .map_err(|e| format!("mkdir {}: {e}", parent.display()))?;
        }
        std::fs::write(&dst, bytes).map_err(|e| format!("write {}: {e}", dst.display()))?;
    }
    let out = Command::new("go")
        .args(["build", "-ldflags=-s -w", "-o"])
        .arg(bin)
        .arg(".")
        .current_dir(cache_root)
        .output()
        .map_err(|e| format!("go build sky-ffi-inspect: {e}"))?;
    if !out.status.success() {
        let _ = src_dir; // (kept for parity with the message context)
        return Err(format!(
            "sky-ffi-inspect: go build failed:\n{}",
            String::from_utf8_lossy(&out.stderr)
        ));
    }
    Ok(bin.to_path_buf())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture(name: &str) -> String {
        let p = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("tests")
            .join("fixtures")
            .join(name);
        std::fs::read_to_string(p).unwrap()
    }

    #[test]
    fn parses_uuid_inspector_json() {
        let info = parse_one(&fixture("uuid.inspector.json")).unwrap();
        assert_eq!(info.pkg, "github.com/google/uuid");
        assert_eq!(info.name, "uuid");
        assert!(info.functions.iter().any(|f| f.name == "NewString"));
        assert!(!info.implements.is_empty());
    }

    #[test]
    fn normalize_is_idempotent_and_sorts_implements() {
        let mut a = parse_one(&fixture("uuid.inspector.json")).unwrap();
        normalize(&mut a);
        let mut b = a.clone();
        normalize(&mut b);
        assert_eq!(a, b, "normalize must be idempotent");
        for vs in a.implements.values() {
            let mut sorted = vs.clone();
            sorted.sort();
            assert_eq!(vs, &sorted, "implements slices must be sorted");
        }
        // functions sorted by name.
        let names: Vec<&str> = a.functions.iter().map(|f| f.name.as_str()).collect();
        let mut sorted = names.clone();
        sorted.sort();
        assert_eq!(names, sorted, "functions must be name-sorted");
    }

    #[test]
    fn is_writable_dir_probes_correctly() {
        // #7: a creatable temp dir is writable; a path under a read-only root is
        // not — this is the probe the cache-dir fallback relies on.
        let tmp = std::env::temp_dir().join(format!(
            "sky-writable-probe-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos())
                .unwrap_or(0)
        ));
        assert!(super::is_writable_dir(&tmp), "fresh temp dir must be writable");
        let _ = std::fs::remove_dir_all(&tmp);

        // A path whose parent cannot be created (root is read-only on any sane CI
        // host) must probe as NOT writable, so selection falls through.
        assert!(
            !super::is_writable_dir(std::path::Path::new(
                "/proc/sky-cannot-create-here/nested"
            )),
            "an uncreatable path must be reported unwritable"
        );
    }

    #[test]
    fn env_unset_or_empty_treats_empty_as_unset() {
        // #7: an empty GOCACHE must be treated as unset (Go would fall back to its
        // HOME default otherwise). Uses a unique key so it can't race other tests.
        let key = format!("SKY_TEST_ENVCHECK_{}", std::process::id());
        assert!(super::env_unset_or_empty(&key), "absent → true");
        std::env::set_var(&key, "");
        assert!(super::env_unset_or_empty(&key), "empty → true");
        std::env::set_var(&key, "x");
        assert!(!super::env_unset_or_empty(&key), "non-empty → false");
        std::env::remove_var(&key);
    }
}
