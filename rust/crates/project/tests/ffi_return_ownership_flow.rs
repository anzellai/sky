//! Explicit heavy integration probe: real generated format-3 FFI + typed Sky.
//! The same staged source can be compiled as raw wasm, bypassing auto-split's
//! server/RPC copy boundary. Kept opt-in because it compiles a full runtime.
use std::path::{Path, PathBuf};
use std::process::Command;

fn root() -> PathBuf {
    let mut p = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !p.join("runtime-go/rt").is_dir() {
        assert!(p.pop());
    }
    p
}
fn copy_production(src: &Path, dst: &Path) {
    std::fs::create_dir_all(dst).unwrap();
    for entry in std::fs::read_dir(src).unwrap() {
        let entry = entry.unwrap();
        let name = entry.file_name();
        let path = entry.path();
        if path.is_dir() {
            if name == "ai_train_assets" {
                copy_assets(&path, &dst.join(name));
            } else if name != "console_app" && name != "testdata" {
                copy_production(&path, &dst.join(name));
            }
        } else if name.to_string_lossy().ends_with(".go")
            && !name.to_string_lossy().ends_with("_test.go")
        {
            std::fs::copy(&path, dst.join(name)).unwrap();
        }
    }
}
fn copy_assets(src: &Path, dst: &Path) {
    std::fs::create_dir_all(dst).unwrap();
    for entry in std::fs::read_dir(src).unwrap() {
        let entry = entry.unwrap();
        if entry.file_name().to_string_lossy().starts_with('.') {
            continue;
        }
        if entry.path().is_dir() {
            copy_assets(&entry.path(), &dst.join(entry.file_name()));
        } else {
            std::fs::copy(entry.path(), dst.join(entry.file_name())).unwrap();
        }
    }
}
fn go(stage: &Path, args: &[&str], wasm: bool) -> std::process::Output {
    let mut command = Command::new("go");
    command
        .current_dir(stage)
        .args(args)
        .stdin(std::process::Stdio::null());
    if wasm {
        command.env("GOOS", "js").env("GOARCH", "wasm");
    }
    command.output().expect("Go toolchain")
}

#[test]
#[ignore = "explicit full native+raw-wasm compiled FFI ownership probe"]
fn generated_typed_ffi_retains_list_values_after_producer_reuse() {
    let root = root();
    let fixtures = root.join("rust/crates/ffi/tests/fixtures/return-ownership");
    let unique = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let dir = std::env::temp_dir().join(format!("sky-ffi-owned-{}-{unique}", std::process::id()));
    let app = dir.join("project");
    let stage = dir.join("go-stage");
    let producer = dir.join("producer");
    for path in [
        app.join("src"),
        app.join("sky-ffi/go"),
        stage.join("skyffi"),
        producer.clone(),
    ] {
        std::fs::create_dir_all(path).unwrap();
    }
    std::fs::write(app.join("sky.toml"), "name = \"ffi-owned\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n[\"go.dependencies\"]\n\"example.test/reusedbuffers\" = \"v0.0.0\"\n").unwrap();
    std::fs::copy(fixtures.join("Main.sky"), app.join("src/Main.sky")).unwrap();
    let info: ffi::PackageInfo = serde_json::from_str(
        &std::fs::read_to_string(fixtures.join("package.inspector.json")).unwrap(),
    )
    .unwrap();
    let generated = ffi::generate(&info);
    std::fs::write(
        app.join(format!("sky-ffi/{}.kernel.json", generated.slug)),
        &generated.kernel_json,
    )
    .unwrap();
    std::fs::write(
        app.join(format!("sky-ffi/{}.skyi", generated.slug)),
        &generated.skyi,
    )
    .unwrap();
    std::fs::write(
        app.join(format!("sky-ffi/go/{}_bindings.go", generated.slug)),
        &generated.bindings_go,
    )
    .unwrap();
    let source =
        project::emit_example_source(&root, &app).expect("typed Sky ownership fixture must emit");
    assert!(
        source.contains("skyffi.Go_Reusedbuffers_readT"),
        "compiled fixture must reach typed FFI wrapper"
    );
    assert!(
        source.contains("skyffi.Go_Reusedbuffers_feedT"),
        "compiled fixture must reach exact-ABI callback wrapper"
    );
    std::fs::write(stage.join("main.go"), source).unwrap();
    std::fs::write(
        stage.join("skyffi/reusedbuffers_bindings.go"),
        generated.bindings_go,
    )
    .unwrap();
    copy_production(&root.join("runtime-go/rt"), &stage.join("rt"));
    std::fs::copy(fixtures.join("buffers.go"), producer.join("buffers.go")).unwrap();
    std::fs::write(
        producer.join("go.mod"),
        "module example.test/reusedbuffers\n\ngo 1.25.0\n",
    )
    .unwrap();
    let mut go_mod = std::fs::read_to_string(root.join("runtime-go/go.mod")).unwrap();
    go_mod.push_str(&format!(
        "\nrequire example.test/reusedbuffers v0.0.0\nreplace example.test/reusedbuffers => {}\n",
        producer.display()
    ));
    std::fs::write(stage.join("go.mod"), go_mod).unwrap();
    std::fs::copy(root.join("runtime-go/go.sum"), stage.join("go.sum")).unwrap();
    let built = go(&stage, &["build", "-o", "probe", "."], false);
    assert!(
        built.status.success(),
        "native fixture build failed: {}",
        String::from_utf8_lossy(&built.stderr)
    );
    eprintln!("FFI ownership artifacts: {}", stage.display());
    let ran = Command::new(stage.join("probe")).output().unwrap();
    let output = format!(
        "{}{}",
        String::from_utf8_lossy(&ran.stdout),
        String::from_utf8_lossy(&ran.stderr)
    );
    assert!(
        ran.status.success() && output.contains("FFI_OWNERSHIP_PASS"),
        "retained data changed: {output}"
    );
    let wasm = go(&stage, &["build", "-o", "probe.wasm", "."], true);
    assert!(
        wasm.status.success(),
        "raw wasm fixture build failed: {}",
        String::from_utf8_lossy(&wasm.stderr)
    );
    // Raw wasm execution remains a separate browser/Node harness gate. This
    // test deliberately claims compilation only for that target.
    // Keep this opt-in probe's small project/stage (printed before native
    // execution, including failures) for diagnosis and the separately claimed
    // raw-wasm execution gate. The root reviewer removes the printed directory.
}
