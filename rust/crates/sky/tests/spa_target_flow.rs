//! `sky build --target <t>` — the Sky.Spa multi-target build pipeline.
//!
//! The `web` target is the one every platform's shell wraps and the only one
//! buildable with no native SDK, so it is the portable regression: it must
//! compile the client to wasm and stage a servable bundle (index.html +
//! main.wasm + wasm_exec.js) under `dist/`. The desktop/ios/android shells are
//! covered end-to-end by the example scaffolds + the earlier manual verification
//! (they need cgo-WebKit / the Android SDK / full Xcode, gated elsewhere); here
//! we pin the shape the whole pipeline rests on.

use std::path::PathBuf;
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-spatarget-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"spatarget\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    // A minimal client — the pipeline shape does not depend on the app being a
    // full Sky.Spa TEA loop, only on it compiling to wasm.
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\nmain =\n    println \"spa\"\n",
    )
    .unwrap();
    dir
}

#[test]
fn target_web_stages_a_servable_wasm_bundle() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("web");
    let out = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build --target web");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(out.status.success(), "build --target web failed:\n{log}");

    // The wasm client + its JS bootstrap land in sky-out/.
    assert!(dir.join("sky-out").join("main.wasm").is_file(), "no sky-out/main.wasm:\n{log}");
    assert!(
        dir.join("sky-out").join("wasm_exec.js").is_file(),
        "no sky-out/wasm_exec.js:\n{log}"
    );
    // …and the servable bundle is staged under dist/ (the wasm is CONTENT-HASHED,
    // main.<hash>.wasm, so a redeploy is never served a stale cached copy).
    let dist = dir.join("dist");
    for f in ["index.html", "wasm_exec.js"] {
        assert!(dist.join(f).is_file(), "dist/{f} missing:\n{log}");
    }
    let hashed = std::fs::read_dir(&dist)
        .unwrap()
        .filter_map(|e| e.ok())
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .find(|n| n.starts_with("main.") && n.ends_with(".wasm"))
        .unwrap_or_else(|| panic!("no content-hashed main.<hash>.wasm in dist:\n{log}"));
    // index.html must actually bootstrap THAT wasm (not an empty placeholder).
    let index = std::fs::read_to_string(dist.join("index.html")).unwrap();
    assert!(
        index.contains("wasm_exec.js") && index.contains(&hashed),
        "index.html does not bootstrap the hashed wasm ({hashed}):\n{index}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn unknown_target_is_rejected_before_building() {
    let dir = scratch("badtarget");
    let out = Command::new(SKY)
        .args(["build", "--target", "frobnicate", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("run sky build --target frobnicate");
    assert!(!out.status.success(), "an unknown target must fail");
    let err = String::from_utf8_lossy(&out.stderr);
    assert!(
        err.contains("unknown target") && err.contains("frobnicate"),
        "the error must name the bad target and the supported set:\n{err}"
    );
    // It must fail FAST — before writing any wasm.
    assert!(
        !dir.join("sky-out").join("main.wasm").is_file(),
        "an unknown target must not have run the wasm build"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
