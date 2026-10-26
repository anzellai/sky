//! `Std.Ai.Train` end to end, offline, on every commit.
//!
//! Test mode (`SKY_TEST_MODE=1`, or a project's `.env.test` under `sky test`)
//! runs the durable training workflow for real on SQLite while the trainer is
//! simulated: the runtime writes the protocol a Fake trainer writes, real
//! verified checkpoints and a real (no-op) PEFT adapter, with no Python, no
//! model and no network (runtime-go/rt/ai_train.go, `aiTrainSimulate`). So
//! these tests prove the workflow, the relaunch from the newest checkpoint, the
//! GGUF conversion, the registry and the `sky ai train` verbs in seconds. The
//! real trainers are the live tests `ai_train_live.rs` (Fake under the managed
//! Python) and `ai_train_native_live.rs` (the native trainer on Apple silicon).

use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// A unique suffix for a scratch directory. The clock alone is not unique:
/// macOS gives microseconds, so two tests that start together got the SAME
/// directory and shared one fake engine (ai_cli_flow counted another test's
/// launches). The counter makes every name distinct within the process.
fn unique_suffix() -> String {
    use std::sync::atomic::{AtomicUsize, Ordering};
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    format!(
        "{}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )
}

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..")
}

fn scratch(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("sky-ai-train-{tag}-{}", unique_suffix()));
    std::fs::create_dir_all(&d).unwrap();
    d
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let name = entry.file_name();
        let s = name.to_string_lossy();
        if s == "sky-out" || s == ".skyapp" || s == ".sky" || s.ends_with(".db") {
            continue;
        }
        let dest = to.join(&name);
        if entry.file_type().unwrap().is_dir() {
            copy_dir(&entry.path(), &dest);
        } else {
            std::fs::copy(entry.path(), dest).unwrap();
        }
    }
}

fn text(out: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    )
}

/// `sky ai <args>` in `dir`, with its own AI home, no inherited training or
/// slot variables, and test mode on or off.
fn sky_ai(dir: &Path, home: &Path, test_mode: bool, args: &[&str], env: &[(&str, &str)]) -> Output {
    let mut cmd = Command::new(SKY);
    cmd.arg("ai")
        .args(args)
        .current_dir(dir)
        .env("SKY_AI_HOME", home)
        .env_remove("SKY_AI_TRAIN_BACKEND")
        .env_remove("SKY_AI_LLAMA_BIN")
        .env_remove("SKY_AI_TRAINER_BIN")
        .env_remove("SKY_AI_REGISTRY")
        .env_remove("SKY_DB_PATH")
        .env_remove("DATABASE_URL")
        .stdin(Stdio::null());
    if test_mode {
        cmd.env("SKY_TEST_MODE", "1");
    } else {
        cmd.env_remove("SKY_TEST_MODE");
    }
    for (k, v) in env {
        cmd.env(k, v);
    }
    cmd.output().expect("run sky ai")
}

fn json(out: &Output) -> serde_json::Value {
    let stdout = String::from_utf8_lossy(&out.stdout);
    let line = stdout.trim().lines().last().unwrap_or("");
    serde_json::from_str(line).unwrap_or_else(|e| panic!("not JSON ({e}):\n{}", text(out)))
}

/// A project directory holding the example's data, frozen into
/// `.sky/ai/datasets/support` by `sky ai data freeze`.
fn frozen_project(tag: &str, home: &Path) -> PathBuf {
    let dir = scratch(tag);
    std::fs::copy(
        repo_root().join("examples/73-ai-train/data/support.jsonl"),
        dir.join("support.jsonl"),
    )
    .unwrap();
    let out = sky_ai(
        &dir,
        home,
        false,
        &[
            "data",
            "freeze",
            "support.jsonl",
            "--name",
            "support",
            "--out",
            ".sky/ai/datasets",
            "--split",
            "70/30/0",
            "--format",
            "json",
        ],
        &[],
    );
    assert!(out.status.success(), "freeze: {}", text(&out));
    dir
}

#[test]
fn the_train_example_trains_registers_and_serves_offline() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let project = scratch("example");
    let home = scratch("example-home");
    copy_dir(&repo_root().join("examples/73-ai-train"), &project);
    assert!(
        project.join(".env.test").exists(),
        "examples/73-ai-train must commit a .env.test so `sky test` runs in test mode"
    );
    let out = Command::new(SKY)
        .args(["test", "tests/TrainExampleTest.sky"])
        .current_dir(&project)
        .env("SKY_AI_HOME", &home)
        .env("SKY_AI_FETCH", "off")
        .env_remove("SKY_AI_TRAIN_BACKEND")
        .env_remove("SKY_AI_LLAMA_BIN")
        .env_remove("SKY_AI_TRAINER_BIN")
        .stdin(Stdio::null())
        .output()
        .expect("run sky test");
    let report = text(&out);
    assert!(
        out.status.success(),
        "the example's suite must pass offline:\n{report}"
    );
    assert!(report.contains("7 passed, 0 failed"), "{report}");
    // Nothing was installed or fetched: no toolchain, no python, no model.
    for d in [
        "toolchains",
        "python",
        "tools",
        "bases",
        "models",
        "engines",
    ] {
        assert!(
            !home.join(d).exists(),
            "test mode wrote {d}/ into the AI home"
        );
    }
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&home);
}

#[test]
fn sky_ai_train_runs_reports_exports_and_cancels_in_test_mode() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let home = scratch("cli-home");
    let dir = frozen_project("cli", &home);
    let out = sky_ai(
        &dir,
        &home,
        true,
        &[
            "train",
            "--data",
            ".sky/ai/datasets/support",
            "--backend",
            "fake",
            "--steps",
            "6",
            "--gguf",
            "--format",
            "json",
        ],
        &[],
    );
    assert!(out.status.success(), "sky ai train: {}", text(&out));
    let a = json(&out);
    assert_eq!(a["backend"], "fake", "{a}");
    assert_eq!(a["steps"], 6, "{a}");
    assert_eq!(a["dataset"], "support", "{a}");
    let run = a["run"].as_str().unwrap().to_string();
    assert!(run.starts_with("support-"), "{run}");
    let gguf = PathBuf::from(a["gguf"].as_str().expect("a GGUF adapter"));
    assert!(gguf.is_file(), "{}", gguf.display());
    assert!(dir
        .join(".sky/ai/runs")
        .join(&run)
        .join("cli.json")
        .is_file());

    // The run's progress and the run list read the run directory.
    let st = json(&sky_ai(
        &dir,
        &home,
        true,
        &["train", "status", &run, "--format", "json"],
        &[],
    ));
    assert_eq!(st["status"], "done", "{st}");
    assert_eq!(st["step"], 6, "{st}");
    let runs = json(&sky_ai(
        &dir,
        &home,
        true,
        &["train", "runs", "--format", "json"],
        &[],
    ));
    assert_eq!(runs.as_array().map(|r| r.len()), Some(1), "{runs}");
    assert_eq!(runs[0]["run"], run.as_str(), "{runs}");

    // The same command again finds the finished run: same adapter, no training.
    let again = sky_ai(
        &dir,
        &home,
        true,
        &[
            "train",
            "--data",
            ".sky/ai/datasets/support",
            "--backend",
            "fake",
            "--steps",
            "6",
            "--gguf",
            "--format",
            "json",
        ],
        &[],
    );
    assert!(again.status.success(), "{}", text(&again));
    assert_eq!(json(&again)["peftSha256"], a["peftSha256"]);

    // export writes the GGUF adapter where it is asked to.
    let out = sky_ai(
        &dir,
        &home,
        true,
        &["export", &run, "--out", "adapter.gguf", "--format", "json"],
        &[],
    );
    assert!(out.status.success(), "export: {}", text(&out));
    assert!(dir.join("adapter.gguf").is_file());
    assert_eq!(json(&out)["sha256"], a["ggufSha256"]);

    // cancel of a finished run signals nothing.
    let out = sky_ai(
        &dir,
        &home,
        true,
        &["train", "cancel", &run, "--format", "json"],
        &[],
    );
    assert!(out.status.success(), "cancel: {}", text(&out));
    assert_eq!(json(&out)["signalled"], false);

    let _ = std::fs::remove_dir_all(&dir);
    let _ = std::fs::remove_dir_all(&home);
}

#[test]
fn a_missing_toolchain_with_fetching_off_names_the_provision_command() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let home = scratch("fetchoff-home");
    let dir = frozen_project("fetchoff", &home);
    let out = sky_ai(
        &dir,
        &home,
        false,
        &[
            "train",
            "--data",
            ".sky/ai/datasets/support",
            "--backend",
            "fake",
            "--steps",
            "4",
        ],
        &[("SKY_AI_FETCH", "off")],
    );
    assert!(!out.status.success(), "{}", text(&out));
    let t = text(&out);
    assert!(t.contains("NotProvisioned"), "{t}");
    assert!(t.contains("sky ai provision --train fake"), "{t}");
    assert!(!home.join("toolchains").exists() && !home.join("python").exists());
    let _ = std::fs::remove_dir_all(&dir);
    let _ = std::fs::remove_dir_all(&home);
}

/// `sky ai provision --train mlx` provisions the native trainer, never a
/// Python toolchain: the operator's `SKY_AI_TRAINER_BIN` is checked
/// (`--version`) and reported, and without it a host with no pinned bundle is
/// `NotProvisioned` naming the variable.
#[cfg(unix)]
#[test]
fn provision_train_mlx_takes_the_operators_native_trainer() {
    use std::os::unix::fs::PermissionsExt;
    if !required(Need::Go, have_go()) {
        return;
    }
    let home = scratch("trainer-home");
    let dir = scratch("trainer");
    let bin = dir.join("sky-trainer");
    std::fs::write(
        &bin,
        "#!/bin/sh\ncase \"$1\" in --version) echo 'sky-trainer 0.1.0 (mlx-c)' ;; *) exit 3 ;; esac\n",
    )
    .unwrap();
    std::fs::set_permissions(&bin, std::fs::Permissions::from_mode(0o755)).unwrap();
    let b = bin.to_string_lossy().to_string();
    let out = sky_ai(
        &dir,
        &home,
        false,
        &["provision", "--train", "mlx", "--format", "json"],
        &[("SKY_AI_TRAINER_BIN", &b), ("SKY_AI_FETCH", "off")],
    );
    assert!(out.status.success(), "provision: {}", text(&out));
    let j = json(&out);
    assert_eq!(j["toolchain"], "mlx", "{j}");
    assert_eq!(j["trainer"], b.as_str(), "{j}");
    assert_eq!(j["versions"]["sky-trainer"], "0.1.0", "{j}");
    assert!(
        !home.join("toolchains").exists() && !home.join("python").exists(),
        "no Python toolchain is installed for mlx"
    );

    let out = sky_ai(
        &dir,
        &home,
        false,
        &["provision", "--train", "mlx", "--format", "json"],
        &[("SKY_AI_FETCH", "off")],
    );
    assert!(!out.status.success(), "{}", text(&out));
    let t = text(&out);
    assert!(t.contains("NotProvisioned"), "{t}");
    assert!(t.contains("SKY_AI_TRAINER_BIN"), "{t}");
    let _ = std::fs::remove_dir_all(&dir);
    let _ = std::fs::remove_dir_all(&home);
}

/// `sky ai train --base` takes any base the library takes: a Hub repository at
/// its commit (`hf:<org>/<repo>@<commit>`) or a local directory
/// (`file:<dir>`), each with `--serving`, the model that serves the adapter.
/// A malformed base is the library's typed `InvalidSetting`, before anything
/// trains.
#[test]
fn sky_ai_train_takes_a_hub_or_local_base_and_refuses_a_malformed_one() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let home = scratch("anybase-home");
    let dir = frozen_project("anybase", &home);
    let serving = "hf:Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_0.gguf\
                   #sha256=7671c0c304e6ce5a7fc577bcb12aba01e2c155cc2efd29b2213c95b18edaf6ed";
    let commit = "7ae557604adf67be50417f59c2c2f167def9a775";
    let train = |base: &str, extra: &[&str]| {
        let mut args = vec![
            "train",
            "--data",
            ".sky/ai/datasets/support",
            "--backend",
            "fake",
            "--steps",
            "3",
            "--base",
            base,
        ];
        args.extend_from_slice(extra);
        args.extend_from_slice(&["--format", "json"]);
        sky_ai(&dir, &home, true, &args, &[])
    };
    let job_of = |out: &Output| -> String {
        let run = json(out)["run"].as_str().unwrap().to_string();
        std::fs::read_to_string(dir.join(".sky/ai/runs").join(&run).join("job.json"))
            .expect("job.json")
    };

    // A Hub base at its commit.
    let hub = format!("hf:Qwen/Qwen2.5-1.5B-Instruct@{commit}");
    let out = train(&hub, &["--serving", serving, "--name", "hub"]);
    assert!(out.status.success(), "hf: base: {}", text(&out));
    let job = job_of(&out);
    assert!(
        job.contains(&format!("Qwen/Qwen2.5-1.5B-Instruct@{commit}")),
        "the job names the Hub base: {job}"
    );

    // A local directory of weights, pinned by hashing it.
    let weights = scratch("anybase-weights");
    std::fs::write(weights.join("config.json"), r#"{"model_type":"qwen2"}"#).unwrap();
    std::fs::write(weights.join("model.safetensors"), b"weights").unwrap();
    let local = format!("file:{}", weights.display());
    let out = train(&local, &["--serving", serving, "--name", "local"]);
    assert!(out.status.success(), "file: base: {}", text(&out));
    let job = job_of(&out);
    let at = job
        .find(&format!("{local}@"))
        .unwrap_or_else(|| panic!("the job names the local base with its digest: {job}"));
    let digest: String = job[at + local.len() + 1..].chars().take(64).collect();
    assert!(
        digest.len() == 64 && digest.chars().all(|c| c.is_ascii_hexdigit()),
        "{job}"
    );
    // Other weights in the same directory are another run, not a resume.
    let first_run = json(&out)["run"].as_str().unwrap().to_string();
    std::fs::write(weights.join("model.safetensors"), b"other weights").unwrap();
    let out = train(&local, &["--serving", serving, "--name", "local"]);
    assert!(out.status.success(), "file: base again: {}", text(&out));
    assert_ne!(json(&out)["run"].as_str().unwrap(), first_run);

    // Malformed bases: the library's typed refusal, naming the form.
    for bad in [
        "hf:Qwen/Qwen2.5-1.5B-Instruct",
        "hf:Qwen/Qwen2.5-1.5B-Instruct@main",
        "Qwen/Qwen2.5-1.5B-Instruct",
        "file:",
    ] {
        let out = train(bad, &["--serving", serving]);
        let t = text(&out);
        assert!(!out.status.success(), "{bad} was accepted: {t}");
        assert!(
            t.contains("InvalidSetting") && t.contains("hf:<org>/<repo>@<40-digit commit>"),
            "{bad}: {t}"
        );
    }
    // A local directory with no weights.
    let empty = scratch("anybase-empty");
    let out = train(
        &format!("file:{}", empty.display()),
        &["--serving", serving],
    );
    assert!(
        !out.status.success() && text(&out).contains("safetensors"),
        "{}",
        text(&out)
    );
    // A base other than dev without the model that serves it.
    let out = train(&hub, &[]);
    assert!(
        !out.status.success() && text(&out).contains("--serving"),
        "{}",
        text(&out)
    );

    for d in [&dir, &home, &weights, &empty] {
        let _ = std::fs::remove_dir_all(d);
    }
}

#[test]
fn a_bad_backend_variable_is_refused_naming_it() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let home = scratch("badbackend-home");
    let dir = frozen_project("badbackend", &home);
    let out = sky_ai(
        &dir,
        &home,
        true,
        &[
            "train",
            "--data",
            ".sky/ai/datasets/support",
            "--steps",
            "4",
        ],
        &[("SKY_AI_TRAIN_BACKEND", "tpu")],
    );
    assert!(!out.status.success(), "{}", text(&out));
    let t = text(&out);
    assert!(
        t.contains("InvalidSetting") && t.contains("SKY_AI_TRAIN_BACKEND"),
        "{t}"
    );

    let out = sky_ai(
        &dir,
        &home,
        true,
        &[
            "train",
            "--data",
            ".sky/ai/datasets/support",
            "--steps",
            "4",
        ],
        &[("SKY_AI_TRAIN_BACKEND", "sagemaker")],
    );
    assert!(
        !out.status.success() && text(&out).contains("SageMaker"),
        "{}",
        text(&out)
    );

    if cfg!(target_os = "macos") {
        let out = sky_ai(
            &dir,
            &home,
            true,
            &[
                "train",
                "--data",
                ".sky/ai/datasets/support",
                "--backend",
                "unsloth",
                "--steps",
                "4",
            ],
            &[],
        );
        let t = text(&out);
        assert!(
            !out.status.success() && t.contains("NoAccelerator") && t.contains("CUDA"),
            "{t}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
    let _ = std::fs::remove_dir_all(&home);
}

/// Regression: several `sky ai` processes that find a cold bundled-app cache
/// at once each build in a staging directory and one rename wins. Before,
/// they materialised into the same directory and one deleted the source tree
/// another was reading ("could not materialise bundled source").
#[test]
fn concurrent_sky_ai_runs_share_a_cold_bundle_cache() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let cache = scratch("cold-cache");
    let home = scratch("cold-home");
    let work = scratch("cold-work");
    let gocache = Command::new("go")
        .args(["env", "GOCACHE"])
        .output()
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_default();
    let children: Vec<_> = (0..3)
        .map(|_| {
            let mut cmd = Command::new(SKY);
            cmd.args(["ai", "train", "runs", "--format", "json"])
                .current_dir(&work)
                .env("XDG_CACHE_HOME", &cache)
                .env("SKY_AI_HOME", &home)
                .stdin(Stdio::null())
                .stdout(Stdio::piped())
                .stderr(Stdio::piped());
            if !gocache.is_empty() {
                cmd.env("GOCACHE", &gocache);
            }
            cmd.spawn().expect("spawn sky ai")
        })
        .collect();
    for child in children {
        let out = child.wait_with_output().unwrap();
        assert!(out.status.success(), "{}", text(&out));
        assert_eq!(json(&out), serde_json::json!([]), "{}", text(&out));
    }
    let leftovers: Vec<_> = std::fs::read_dir(cache.join("sky"))
        .unwrap()
        .flatten()
        .map(|e| e.file_name().to_string_lossy().to_string())
        .filter(|n| n.contains(".staging-"))
        .collect();
    assert!(
        leftovers.is_empty(),
        "staging directories left: {leftovers:?}"
    );
    for d in [&cache, &home, &work] {
        let _ = std::fs::remove_dir_all(d);
    }
}
