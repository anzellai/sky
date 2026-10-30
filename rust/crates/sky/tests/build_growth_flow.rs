//! The counted build-speed guard: generated Sky and emitted Go grow linearly.
//!
//! `xtask bench-gen` writes the build-speed benchmark app (a medium `Std.App`
//! web app: a wide Model, many server and pure `update` arms, whole-model
//! arms, a typed form, `Std.Db.Store`, `Std.Auth`, `Std.Ui` views over a
//! layered module DAG). This test generates it at N and at 2N Model fields
//! (and `update` arms), and bounds how much the compiler's output grows:
//!
//!   * the Sky the Sky.Spa split generates (`sky spa-split`, the split `sky
//!     build --target web:app` runs): `Shared.sky` and the two generated
//!     entries, counted in lines (the copied user modules are linear by
//!     construction and would only dilute the ratio);
//!   * the Go the compiler emits for the split backend, the wasm frontend and
//!     the `--target web` program, counted in bytes.
//!
//! Linear output doubles. A per-arm copy of the Model's field list, the shape
//! that made a medium app's split 156k lines and its backend leg 86 s before
//! the wire dedupe, grows as arms x fields (about 4x) and fails the bound.
//! Counted, not timed, so the verdict is the same on every machine. Wall time
//! is `scripts/bench-build.sh`'s job (the `build-perf` CI job).
//!
//! The new-directory property (a copy of a built project compiles zero Go
//! packages) is `go_build_cache_flow::a_copy_in_a_new_directory_compiles_nothing`.
//! Both are registered with the gate harness, each with a falsifying mutation.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../../xtask/src/bench_app.rs"]
#[allow(dead_code)]
mod bench_app;

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// Allowed growth from N to 2N. Linear output doubles; the margin covers the
/// seeded field mix and the per-arm aliases a shared wire shape still needs.
const BOUND: f64 = 2.3;
/// N. Large enough that a per-arm field list outweighs the fixed parts.
const N: usize = 80;

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .canonicalize()
        .expect("repo root")
}

fn scratch(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "sky-build-growth-{tag}-{}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// Lines of every `.sky` file under `dir`.
fn sky_lines(dir: &Path) -> usize {
    let mut n = 0;
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        for e in std::fs::read_dir(&d).unwrap().flatten() {
            let p = e.path();
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().and_then(|x| x.to_str()) == Some("sky") {
                n += std::fs::read_to_string(&p).unwrap().lines().count();
            }
        }
    }
    n
}

fn emitted_go_bytes(project: &Path) -> usize {
    project::emit_example_source(&repo_root(), project)
        .unwrap_or_else(|e| panic!("emit {}:\n{e}", project.display()))
        .len()
}

#[derive(Debug, Clone, Copy)]
struct Size {
    user_lines: usize,
    split_lines: usize,
    /// The files the split writes itself: `Shared.sky` and the two entries.
    generated_lines: usize,
    go_bytes: usize,
}

fn measure(n: usize) -> Size {
    let dir = scratch(&n.to_string());
    let app = dir.join("app");
    let params = bench_app::Params {
        fields: n,
        arms: n,
        modules: 8,
        seed: 1,
    };
    let user_lines = bench_app::write(params, &app).unwrap();
    let out = Command::new(SKY)
        .args(["spa-split", "src/Main.sky", "--out", ".split"])
        .current_dir(&app)
        .output()
        .expect("run sky spa-split");
    assert!(
        out.status.success(),
        "sky spa-split at N={n}:\n{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let split = app.join(".split");
    let split_lines = sky_lines(&split);
    let generated_lines = [
        "shared/Shared.sky",
        "backend/src/Main.sky",
        "frontend/src/Main.sky",
    ]
    .iter()
    .map(|f| {
        std::fs::read_to_string(split.join(f))
            .unwrap()
            .lines()
            .count()
    })
    .sum();
    let mut go_bytes =
        emitted_go_bytes(&split.join("backend")) + emitted_go_bytes(&split.join("frontend"));
    // `--target web`: the same source with `App.run` bound to the Sky.Live
    // runner, as the build stages it.
    let web = dir.join("web");
    for (rel, text) in bench_app::generate(params).unwrap() {
        let text = if rel == "src/Main.sky" {
            project::app_entry::rewrite_run(&text, "runLive")
        } else {
            text
        };
        let path = web.join(rel);
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, text).unwrap();
    }
    go_bytes += emitted_go_bytes(&web);
    let _ = std::fs::remove_dir_all(&dir);
    Size {
        user_lines,
        split_lines,
        generated_lines,
        go_bytes,
    }
}

#[test]
fn generated_sky_and_emitted_go_grow_linearly() {
    // The compiler's recursive walkers want a large stack; `sky` runs them on
    // a 512 MiB thread, so the in-process emit does too.
    let (small, large) = std::thread::Builder::new()
        .stack_size(512 << 20)
        .spawn(|| (measure(N), measure(2 * N)))
        .unwrap()
        .join()
        .unwrap();
    let ratio = |a: usize, b: usize| b as f64 / a as f64;
    eprintln!("N={N}: {small:?}\nN={}: {large:?}", 2 * N);
    // The generator is linear, or the bound below says nothing.
    assert!(
        ratio(small.user_lines, large.user_lines) < BOUND,
        "the benchmark app itself grew {small:?} -> {large:?}"
    );
    assert!(
        ratio(small.generated_lines, large.generated_lines) < BOUND,
        "the Sky the Sky.Spa split generates grew more than linearly from N={N} to N={}: \
         {} -> {} lines ({:.2}x, bound {BOUND}x)",
        2 * N,
        small.generated_lines,
        large.generated_lines,
        ratio(small.generated_lines, large.generated_lines)
    );
    assert!(
        ratio(small.go_bytes, large.go_bytes) < BOUND,
        "the emitted Go grew more than linearly from N={N} to N={}: {} -> {} bytes \
         ({:.2}x, bound {BOUND}x)",
        2 * N,
        small.go_bytes,
        large.go_bytes,
        ratio(small.go_bytes, large.go_bytes)
    );
}
