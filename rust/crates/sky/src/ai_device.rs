//! The Std.Ai.Device engines: where they live, how `sky ai provision
//! --device` installs them, and how a build stages them.
//!
//! The engines are llama.cpp `b11200` with Sky's engine core
//! (`scripts/ai/device/sky_llama.cpp`), built per target by
//! `scripts/ai/device/build-device-bundles.sh`:
//!
//! - `web`: the browser worker's WebGPU and CPU WebAssembly builds
//!   (`web/{webgpu,cpu}/skyllama.{mjs,wasm}`);
//! - `ios`: `ios/SkyLlama.xcframework` (ios-arm64 with Metal,
//!   ios-arm64-simulator on the CPU);
//! - `android`: `android/{arm64-v8a,x86_64}/libskyllama.so`.
//!
//! The `ai-device-bundles` workflow publishes them as the GitHub release
//! [`RELEASE_TAG`], one archive per target, and [`PINS`] holds each
//! archive's SHA-256. `sky ai provision --device <target>` fetches the pinned
//! archive and installs it under the AI home; `--from <dir|archive>` installs
//! a local build of the same script instead (checked against the pin when
//! the pin is set). Nothing a build stages is fetched by the build itself:
//! `sky build` reads only what provisioning installed.

use std::path::{Path, PathBuf};
use std::process::{Command, ExitCode};

/// The pinned llama.cpp build every device engine is compiled from.
pub const LLAMA_BUILD: &str = "b11200";

/// The SHA-256 of llama.cpp's `b11200` source archive
/// (`https://github.com/ggml-org/llama.cpp/archive/refs/tags/b11200.tar.gz`),
/// which the workflow checks before it builds.
pub const LLAMA_SOURCE_SHA256: &str =
    "77d9d947e1dbdbe3eccc95ae10fa5d6abf48dc3395a84673f67d59b6364b6a94";

/// The release the workflow publishes.
pub const RELEASE_TAG: &str = "sky-llama-b11200";

/// The device targets.
pub const TARGETS: [&str; 3] = ["web", "ios", "android"];

/// `(target, archive, SHA-256)` of each published engine archive. A `None`
/// pin is an archive the workflow has not published yet: provisioning then
/// needs `--from` with a local build.
pub const PINS: &[(&str, &str, Option<&str>)] = &[
    ("web", "sky-llama-b11200-web.tar.gz", None),
    ("ios", "sky-llama-b11200-ios.tar.gz", None),
    ("android", "sky-llama-b11200-android.tar.gz", None),
];

/// Where a published archive is downloaded from.
pub fn release_url(file: &str) -> String {
    format!("https://github.com/anzellai/sky/releases/download/{RELEASE_TAG}/{file}")
}

fn env(name: &str) -> Option<String> {
    std::env::var(name).ok()
}

/// The root of the installed engines: `<AI home>/device-engines/b11200`.
pub fn engine_root() -> PathBuf {
    crate::ai_doctor::ai_home("SKY", &env)
        .join("device-engines")
        .join(LLAMA_BUILD)
}

/// One target's installed engine directory.
pub fn engine_dir(target: &str) -> PathBuf {
    engine_root().join(target)
}

/// The files an installed target must hold.
pub fn required_files(target: &str) -> &'static [&'static str] {
    match target {
        "web" => &[
            "cpu/skyllama.mjs",
            "cpu/skyllama.wasm",
            "webgpu/skyllama.mjs",
            "webgpu/skyllama.wasm",
        ],
        "ios" => &["SkyLlama.xcframework/Info.plist"],
        "android" => &["arm64-v8a/libskyllama.so", "x86_64/libskyllama.so"],
        _ => &[],
    }
}

/// Whether a target's engine is installed, complete.
pub fn installed(target: &str) -> bool {
    let dir = engine_dir(target);
    let req = required_files(target);
    !req.is_empty() && req.iter().all(|f| dir.join(f).is_file())
}

/// The message that names how to install a missing engine.
pub fn not_provisioned(target: &str) -> String {
    format!(
        "the Std.Ai.Device {target} engine (llama.cpp {LLAMA_BUILD}) is not installed in {}; \
         run `sky ai provision --device {target}`",
        engine_dir(target).display()
    )
}

fn pin_for(target: &str) -> Option<(&'static str, Option<&'static str>)> {
    PINS.iter()
        .find(|(t, _, _)| *t == target)
        .map(|(_, f, s)| (*f, *s))
}

/// The outcome of a provisioning: where it installed and from what.
pub struct Installed {
    pub dir: PathBuf,
    pub source: String,
}

/// Install one target's engine. `from` is a local build: the build script's
/// output directory (holding `<target>/` or the archive) or an archive.
pub fn provision(target: &str, from: Option<&Path>) -> Result<Installed, String> {
    let Some((file, pin)) = pin_for(target) else {
        return Err(format!(
            "sky ai provision --device: {target:?} is not a device target (web, ios, android)"
        ));
    };
    let root = engine_root();
    std::fs::create_dir_all(&root).map_err(|e| format!("cannot create {}: {e}", root.display()))?;
    let staging = root.join(format!(".{target}.{}.staging", std::process::id()));
    let _ = std::fs::remove_dir_all(&staging);
    std::fs::create_dir_all(&staging)
        .map_err(|e| format!("cannot create {}: {e}", staging.display()))?;
    let result = (|| -> Result<String, String> {
        let archive: Option<PathBuf> = match from {
            Some(p) if p.is_file() => Some(p.to_path_buf()),
            Some(p) if p.join(target).is_dir() => {
                copy_tree(&p.join(target), &staging.join(target))?;
                None
            }
            Some(p) if p.join(file).is_file() => Some(p.join(file)),
            Some(p) => {
                return Err(format!(
                    "{} holds neither {target}/ nor {file}: build it with \
                     scripts/ai/device/build-device-bundles.sh {target} --out <dir>",
                    p.display()
                ))
            }
            None => {
                let Some(sum) = pin else {
                    return Err(format!(
                        "the {RELEASE_TAG} {target} engine is not published yet (its pin in \
                         rust/crates/sky/src/ai_device.rs is empty); build it with \
                         `scripts/ai/device/build-device-bundles.sh {target} --llama-src <dir> --out <dir>` \
                         and install it with `sky ai provision --device {target} --from <dir>`"
                    ));
                };
                let dest = staging.join(file);
                crate::db_provision::fetch_url(&release_url(file), &dest)?;
                verify(&dest, sum, &release_url(file))?;
                Some(dest)
            }
        };
        let mut source = format!(
            "local build {}",
            from.map(|p| p.display().to_string()).unwrap_or_default()
        );
        if let Some(a) = &archive {
            let actual = crate::db_provision::sha256_file(a)?;
            match pin {
                Some(sum) if from.is_some() => {
                    verify(a, sum, &a.display().to_string())?;
                    source = format!("{file} (sha256 {sum}, pinned)");
                }
                Some(sum) => source = format!("{} (sha256 {sum}, pinned)", release_url(file)),
                None => {
                    source = format!(
                        "{} (sha256 {actual}, a local build; no published pin)",
                        a.display()
                    )
                }
            }
            let st = Command::new("tar")
                .arg("-xzf")
                .arg(a)
                .arg("-C")
                .arg(&staging)
                .status()
                .map_err(|e| format!("cannot run tar: {e}"))?;
            if !st.success() {
                return Err(format!("{} is not a readable archive", a.display()));
            }
        }
        let got = staging.join(target);
        for f in required_files(target) {
            if !got.join(f).is_file() {
                return Err(format!("the {target} engine has no {f}"));
            }
        }
        let dest = engine_dir(target);
        let old = root.join(format!(".{target}.{}.old", std::process::id()));
        if dest.exists() {
            std::fs::rename(&dest, &old)
                .map_err(|e| format!("cannot replace {}: {e}", dest.display()))?;
        }
        std::fs::rename(&got, &dest)
            .map_err(|e| format!("cannot install {}: {e}", dest.display()))?;
        let _ = std::fs::remove_dir_all(&old);
        std::fs::write(dest.join(".provenance"), format!("{source}\n"))
            .map_err(|e| format!("cannot write {}: {e}", dest.display()))?;
        Ok(source)
    })();
    let _ = std::fs::remove_dir_all(&staging);
    result.map(|source| Installed {
        dir: engine_dir(target),
        source,
    })
}

fn verify(path: &Path, want: &str, what: &str) -> Result<(), String> {
    let actual = crate::db_provision::sha256_file(path)?;
    if actual != want {
        return Err(crate::db_provision::checksum_mismatch_message(
            what, want, &actual,
        ));
    }
    Ok(())
}

fn copy_tree(from: &Path, to: &Path) -> Result<(), String> {
    std::fs::create_dir_all(to).map_err(|e| format!("cannot create {}: {e}", to.display()))?;
    let rd = std::fs::read_dir(from).map_err(|e| format!("cannot read {}: {e}", from.display()))?;
    for entry in rd.flatten() {
        let p = entry.path();
        let name = entry.file_name();
        let target = to.join(&name);
        let ft = entry.file_type().map_err(|e| e.to_string())?;
        if ft.is_dir() {
            copy_tree(&p, &target)?;
        } else if ft.is_symlink() {
            let link = std::fs::read_link(&p).map_err(|e| e.to_string())?;
            #[cfg(unix)]
            std::os::unix::fs::symlink(&link, &target).map_err(|e| e.to_string())?;
        } else {
            std::fs::copy(&p, &target).map_err(|e| format!("cannot copy {}: {e}", p.display()))?;
        }
    }
    Ok(())
}

/// `sky ai provision --device <web|ios|android> [--from <dir|archive>]`.
pub fn cmd_provision_device(args: &[String]) -> ExitCode {
    let mut target = None;
    let mut from = None;
    let mut check = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--device" => {
                target = args.get(i + 1).cloned();
                i += 2;
            }
            "--from" => {
                from = args.get(i + 1).map(PathBuf::from);
                i += 2;
            }
            "provision" => i += 1,
            "--check" => {
                check = true;
                i += 1;
            }
            other => {
                eprintln!("sky ai provision --device: unknown argument {other:?}");
                return ExitCode::from(2);
            }
        }
    }
    let Some(target) = target else {
        eprintln!(
            "usage: sky ai provision --device web|ios|android|all [--from <dir|archive>] [--check]"
        );
        return ExitCode::from(2);
    };
    let targets: Vec<&str> = if target == "all" {
        TARGETS.to_vec()
    } else {
        vec![target.as_str()]
    };
    if check {
        // `--check`: report whether each engine is installed; install nothing.
        let mut ok = true;
        for t in &targets {
            if installed(t) {
                println!(
                    "sky ai provision --device {t}: installed in {}",
                    engine_dir(t).display()
                );
            } else {
                eprintln!("sky ai provision --device {t}: {}", not_provisioned(t));
                ok = false;
            }
        }
        return if ok {
            ExitCode::SUCCESS
        } else {
            ExitCode::from(1)
        };
    }
    for t in targets {
        match provision(t, from.as_deref()) {
            Ok(done) => println!(
                "sky ai provision --device {t}: installed {} from {}",
                done.dir.display(),
                done.source
            ),
            Err(e) => {
                eprintln!("sky ai provision --device {t}: {e}");
                return ExitCode::from(1);
            }
        }
    }
    ExitCode::SUCCESS
}

// ─── staging into a web build ───────────────────────────────────────────

/// `--ai-web-engine`: which browser engines a `web:app` build stages.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WebEngine {
    WebGpu,
    Cpu,
    Both,
}

impl WebEngine {
    pub fn parse(s: &str) -> Result<WebEngine, String> {
        match s {
            "webgpu" => Ok(WebEngine::WebGpu),
            "cpu" => Ok(WebEngine::Cpu),
            "both" => Ok(WebEngine::Both),
            other => Err(format!(
                "--ai-web-engine {other:?}: use webgpu, cpu or both"
            )),
        }
    }

    fn variants(self) -> &'static [&'static str] {
        match self {
            WebEngine::WebGpu => &["webgpu"],
            WebEngine::Cpu => &["cpu"],
            WebEngine::Both => &["webgpu", "cpu"],
        }
    }
}

/// Stage the browser engines into a frontend dist as content-hashed files
/// (`sky-ai-<variant>.<hash>.{mjs,wasm}`) and return the `sky-ai-engine`
/// meta content the page carries: `{"cpu":{"mjs":…,"wasm":…},…}`. A stale
/// engine file of an earlier build is removed.
pub fn stage_web_engines(dist: &Path, engine: WebEngine) -> Result<String, String> {
    if !installed("web") {
        return Err(not_provisioned("web"));
    }
    if let Ok(rd) = std::fs::read_dir(dist) {
        for e in rd.flatten() {
            let n = e.file_name().to_string_lossy().to_string();
            if n.starts_with("sky-ai-cpu.") || n.starts_with("sky-ai-webgpu.") {
                let _ = std::fs::remove_file(e.path());
            }
        }
    }
    let src = engine_dir("web");
    let mut parts = Vec::new();
    for v in engine.variants() {
        let mjs = std::fs::read(src.join(v).join("skyllama.mjs"))
            .map_err(|e| format!("cannot read the {v} engine: {e}"))?;
        let wasm = std::fs::read(src.join(v).join("skyllama.wasm"))
            .map_err(|e| format!("cannot read the {v} engine: {e}"))?;
        let mut both = mjs.clone();
        both.extend_from_slice(&wasm);
        let hash = &crate::db_provision::sha256_hex(&both)[..12];
        let mjs_name = format!("sky-ai-{v}.{hash}.mjs");
        let wasm_name = format!("sky-ai-{v}.{hash}.wasm");
        std::fs::write(dist.join(&mjs_name), &mjs).map_err(|e| e.to_string())?;
        std::fs::write(dist.join(&wasm_name), &wasm).map_err(|e| e.to_string())?;
        parts.push(format!(
            "\"{v}\":{{\"mjs\":\"/{mjs_name}\",\"wasm\":\"/{wasm_name}\"}}"
        ));
    }
    Ok(format!("{{{}}}", parts.join(",")))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_target_has_a_pin_row_and_required_files() {
        for t in TARGETS {
            assert!(pin_for(t).is_some(), "{t}");
            assert!(!required_files(t).is_empty(), "{t}");
        }
        assert!(pin_for("windows").is_none());
    }

    #[test]
    fn web_engine_flag_values() {
        assert_eq!(WebEngine::parse("both").unwrap(), WebEngine::Both);
        assert_eq!(WebEngine::parse("cpu").unwrap().variants(), &["cpu"]);
        assert!(WebEngine::parse("gpu")
            .unwrap_err()
            .contains("webgpu, cpu or both"));
    }

    #[test]
    fn a_pin_is_sha256_hex_when_set() {
        for (_, _, sum) in PINS {
            if let Some(s) = sum {
                assert!(crate::db_provision::is_sha256_hex(s), "{s}");
            }
        }
        assert!(crate::db_provision::is_sha256_hex(LLAMA_SOURCE_SHA256));
    }
}
