//! `scripts/ai/device/build-device-bundles.sh` reports why it stops.
//!
//! The web build looked for emsdk's bundled Python with
//! `py="$(ls -d "$EMSDK"/python/*/bin 2>/dev/null | head -1)"` under
//! `set -euo pipefail`. Only the macOS emsdk bundles one, so on the Linux
//! runner the `ls` found nothing, exited 2, and pipefail ended the whole
//! script with status 2 and no message: the release gate
//! `gate-ai-device-web` failed with nothing in its log after the Dawn
//! checksum. This runs the script against a stub emsdk with no Python and the
//! wrong emcc, so it must get as far as the version check and say so.

use std::path::PathBuf;
use std::process::Command;

#[test]
fn the_web_build_without_a_bundled_python_reaches_the_emcc_check() {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    let tmp = std::env::temp_dir().join(format!("sky-ai-device-script-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&tmp);
    let llama = tmp.join("llama/include");
    std::fs::create_dir_all(&llama).unwrap();
    std::fs::write(llama.join("llama.h"), "").unwrap();
    let emsdk = tmp.join("emsdk");
    let bin = emsdk.join("stub-bin");
    std::fs::create_dir_all(&bin).unwrap();
    std::fs::write(
        emsdk.join("emsdk_env.sh"),
        format!("export PATH=\"{}:$PATH\"\n", bin.display()),
    )
    .unwrap();
    let emcc = bin.join("emcc");
    std::fs::write(&emcc, "#!/bin/sh\necho 'emcc (Emscripten) 1.0.0 (stub)'\n").unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&emcc, std::fs::Permissions::from_mode(0o755)).unwrap();
    }

    let out = Command::new("bash")
        .arg(root.join("scripts/ai/device/build-device-bundles.sh"))
        .args(["web", "--llama-src"])
        .arg(tmp.join("llama"))
        .arg("--out")
        .arg(tmp.join("out"))
        .arg("--emsdk")
        .arg(&emsdk)
        .output()
        .unwrap();
    let stderr = String::from_utf8_lossy(&out.stderr).into_owned();
    let _ = std::fs::remove_dir_all(&tmp);
    assert_eq!(out.status.code(), Some(2), "stderr: {stderr}");
    assert!(
        stderr.contains("emsdk 6.0.12 is required, got: emcc (Emscripten) 1.0.0"),
        "the script stopped before the emcc check, or without saying why: {stderr:?}"
    );
}
