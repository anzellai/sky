//! The browser engine's export lists agree with the engine core's API.
//!
//! Every function `sky_llama.h` declares is exported from the WebAssembly
//! builds (`scripts/ai/device/CMakeLists.txt`, `EXPORTED_FUNCTIONS`), every
//! exported function the worker calls that can wait on the GPU is a JSPI
//! export of the WebGPU build (`build-device-bundles.sh`, `JSPI_EXPORTS`), and
//! every engine function the worker calls (`runtime-go/rt/ai_device_worker.go`)
//! is exported. v0.28.0: the streaming functions were exported but not JSPI
//! exports, and a streamed answer on WebGPU failed with "trying to suspend
//! without WebAssembly.promising".

use std::collections::BTreeSet;
use std::path::PathBuf;

fn repo() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..")
}

fn read(rel: &str) -> String {
    std::fs::read_to_string(repo().join(rel)).unwrap_or_else(|e| panic!("{rel}: {e}"))
}

fn list_after(text: &str, marker: &str) -> BTreeSet<String> {
    let at = text.find(marker).unwrap_or_else(|| panic!("no {marker}")) + marker.len();
    text[at..]
        .split(|c: char| c.is_whitespace() || c == '"')
        .next()
        .unwrap()
        .split(',')
        .map(|s| s.trim_start_matches('_').to_string())
        .filter(|s| s.starts_with("sky_llama_"))
        .collect()
}

#[test]
fn the_engine_exports_agree_with_the_api_and_the_worker() {
    let header = read("scripts/ai/device/sky_llama.h");
    let declared: BTreeSet<String> = header
        .lines()
        .filter_map(|l| {
            let l = l.trim();
            let at = l.find("sky_llama_")?;
            let name: String = l[at..]
                .chars()
                .take_while(|c| c.is_ascii_alphanumeric() || *c == '_')
                .collect();
            (l.ends_with(");") && l.contains('(')).then_some(name)
        })
        .collect();
    assert!(declared.contains("sky_llama_chat_next"), "{declared:?}");
    let exported = list_after(
        &read("scripts/ai/device/CMakeLists.txt"),
        "-sEXPORTED_FUNCTIONS=",
    );
    let jspi = list_after(
        &read("scripts/ai/device/build-device-bundles.sh"),
        "-sJSPI_EXPORTS=",
    );
    for f in &declared {
        assert!(
            exported.contains(f),
            "{f} is declared but not exported: {exported:?}"
        );
    }
    // Only the device listing and the last error never wait on the GPU.
    let never_waits = [
        "sky_llama_devices",
        "sky_llama_gpu_devices",
        "sky_llama_last_error",
    ];
    for f in &exported {
        if !never_waits.contains(&f.as_str()) {
            assert!(
                jspi.contains(f),
                "{f} can wait on the GPU but is not a JSPI export: {jspi:?}"
            );
        }
    }
    let worker = read("runtime-go/rt/ai_device_worker.go");
    for part in worker.split("call('").skip(1) {
        let name = part.split('\'').next().unwrap();
        if name.starts_with("sky_llama_") {
            assert!(
                exported.contains(name),
                "the worker calls {name}, which is not exported"
            );
        }
    }
}
