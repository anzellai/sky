//! The `sky build` driver (doc 08 §"What codegen guarantees", doc 09 §A.1):
//! parse + resolve + typecheck + lower + emit → write `sky-out/main.go` + `go.mod`,
//! materialise a pruned copy of `runtime-go/rt` beside it, then run `go build`.
//! The runtime tree is copied wholesale, never regenerated (L10).

use hir::SourceDb;
use std::path::{Path, PathBuf};
use std::process::Command;

/// Where a build writes + what to do after emission.
pub struct BuildOptions {
    pub repo_root: PathBuf,
    pub example_dir: PathBuf,
    /// Output dir name under the example (kept distinct from the oracle's
    /// `sky-out/` so a comparison harness can hold both).
    pub out_dir_name: String,
    pub run: bool,
    /// stdin to feed the binary when `run` is set.
    pub stdin: Option<String>,
}

#[derive(Default)]
pub struct BuildReport {
    pub emitted: bool,
    pub go_build_ok: bool,
    pub go_build_stderr: String,
    pub warnings: Vec<String>,
    pub run_ok: Option<bool>,
    pub run_stdout: Option<String>,
    pub run_stderr: Option<String>,
    pub note: String,
}

/// Build one example directory, returning a structured report (never panics).
pub fn build_example(opts: &BuildOptions) -> BuildReport {
    let mut report = BuildReport::default();

    // ---- assemble the source db (stdlib + example src) ----
    let mut db = SourceDb::new();
    let stdlib = load_dir(&opts.repo_root.join("sky-stdlib"));
    if stdlib.is_empty() {
        report.note = "no stdlib under sky-stdlib".into();
        return report;
    }
    for (n, parse) in stdlib {
        db.add_module(&n, parse);
    }
    let locals = load_dir(&opts.example_dir.join("src"));
    if locals.is_empty() {
        report.note = "no .sky under src/".into();
        return report;
    }
    let mut entry = None;
    for (n, parse) in locals {
        let id = db.add_module(&n, parse);
        if n == "Main" || n.ends_with(".Main") || n == "main" {
            entry = Some(id);
        }
    }
    let Some(entry) = entry else {
        report.note = "no entry module named Main".into();
        return report;
    };

    // ---- lower + emit ----
    let cfg = read_sky_toml_config(&opts.example_dir.join("sky.toml"));
    let out = lower::lower_program_cfg(&db, entry, &cfg);
    report.warnings = out.warnings;
    if !out.entry_ok {
        report.note = "lowering found no entry `main`".into();
        return report;
    }
    let source = codegen::emit_program(&out.items);

    // ---- write sky-out + materialise runtime ----
    let out_dir = opts.example_dir.join(&opts.out_dir_name);
    if let Err(e) = write_out(&opts.repo_root, &out_dir, &source) {
        report.note = format!("write failed: {e}");
        return report;
    }
    report.emitted = true;

    // ---- go build ----
    let build = Command::new("go")
        .arg("build")
        .arg("-o")
        .arg("app")
        .arg(".")
        .current_dir(&out_dir)
        .env("GOFLAGS", "-mod=mod")
        .output();
    match build {
        Ok(o) => {
            report.go_build_ok = o.status.success();
            report.go_build_stderr = String::from_utf8_lossy(&o.stderr).to_string();
        }
        Err(e) => {
            report.go_build_stderr = format!("go build spawn failed: {e}");
            return report;
        }
    }

    // ---- run ----
    if opts.run && report.go_build_ok {
        use std::io::Write;
        let mut cmd = Command::new("./app");
        cmd.current_dir(&out_dir);
        cmd.stdin(std::process::Stdio::piped());
        cmd.stdout(std::process::Stdio::piped());
        cmd.stderr(std::process::Stdio::piped());
        if let Ok(mut child) = cmd.spawn() {
            if let Some(input) = &opts.stdin {
                if let Some(si) = child.stdin.as_mut() {
                    let _ = si.write_all(input.as_bytes());
                }
            }
            drop(child.stdin.take());
            match child.wait_with_output() {
                Ok(o) => {
                    report.run_ok = Some(o.status.success());
                    report.run_stdout = Some(String::from_utf8_lossy(&o.stdout).to_string());
                    report.run_stderr = Some(String::from_utf8_lossy(&o.stderr).to_string());
                }
                Err(e) => report.note = format!("run failed: {e}"),
            }
        }
    }

    report
}

/// Minimal `sky.toml` reader for the build-time `init()` defaults. Extracts
/// top-level `port` and the `[database]` `driver`/`path` — enough for the
/// runtime's `SKY_*` fallbacks (`Db.connect ()` resolves `SKY_DB_PATH`). A full
/// TOML parse isn't warranted for these flat keys; unknown shapes are ignored.
fn read_sky_toml_config(path: &Path) -> lower::LowerConfig {
    let mut cfg = lower::LowerConfig::default();
    let Ok(text) = std::fs::read_to_string(path) else {
        return cfg;
    };
    let mut section = String::new();
    for raw in text.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line.starts_with('[') && line.ends_with(']') {
            section = line.trim_matches(['[', ']']).trim().trim_matches('"').to_string();
            continue;
        }
        let Some((k, v)) = line.split_once('=') else {
            continue;
        };
        let key = k.trim();
        let val = v.trim().trim_matches('"').to_string();
        match (section.as_str(), key) {
            ("", "port") => cfg.port = Some(val),
            ("database", "driver") => cfg.extra_defaults.push(("DB_DRIVER".into(), val)),
            ("database", "path") => cfg.extra_defaults.push(("DB_PATH".into(), val)),
            ("live", "port") => cfg.port = Some(val),
            _ => {}
        }
    }
    cfg
}

fn write_out(repo_root: &Path, out_dir: &Path, source: &str) -> std::io::Result<()> {
    std::fs::create_dir_all(out_dir)?;
    std::fs::write(out_dir.join("main.go"), source)?;
    // go.mod / go.sum from the runtime module (module `sky-app`).
    let rt_src = repo_root.join("runtime-go");
    std::fs::copy(rt_src.join("go.mod"), out_dir.join("go.mod"))?;
    let sum = rt_src.join("go.sum");
    if sum.exists() {
        std::fs::copy(sum, out_dir.join("go.sum"))?;
    }
    // materialise a pruned copy of runtime-go/rt (tests stripped).
    let rt_dst = out_dir.join("rt");
    materialise_rt(&rt_src.join("rt"), &rt_dst)?;
    Ok(())
}

/// Copy `rt/` wholesale, skipping `*_test.go` and testdata (doc 09 §A.1). Copies
/// recursively (the runtime has a `console_app/` subtree).
fn materialise_rt(src: &Path, dst: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dst)?;
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let path = entry.path();
        let name = entry.file_name();
        let name_s = name.to_string_lossy().to_string();
        if path.is_dir() {
            // skip nested module dirs that carry their own go.mod would break the
            // build; the console_app is package main — skip it (not linked by rt).
            if name_s == "console_app" || name_s == "testdata" {
                continue;
            }
            materialise_rt(&path, &dst.join(&name_s))?;
        } else if name_s.ends_with("_test.go") {
            continue;
        } else if name_s.ends_with(".go") {
            std::fs::copy(&path, dst.join(&name_s))?;
        }
    }
    Ok(())
}

// ---- module loading (mirrors xtask/infer_gate) ---------------------------

fn load_dir(dir: &Path) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = module_name(&parse, &path);
        out.push((name, parse));
    }
    out
}

fn module_name(parse: &syntax::Parse, path: &Path) -> String {
    let tree = parse.tree();
    if let Some(n) = tree.module_header().and_then(|h| h.name()).map(|n| n.text()) {
        if !n.is_empty() {
            return n;
        }
    }
    path.file_stem()
        .and_then(|s| s.to_str())
        .unwrap_or("Main")
        .to_string()
}

fn is_generated(path: &Path) -> bool {
    path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out") | Some("sky-out-rust") | Some(".skycache") | Some(".skydeps")
        )
    })
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<PathBuf> = match std::fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok().map(|e| e.path())).collect(),
        Err(_) => return,
    };
    entries.sort();
    for path in entries {
        if is_generated(&path) {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}
