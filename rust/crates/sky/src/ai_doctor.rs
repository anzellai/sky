//! `sky doctor`'s Std.Ai checks: the slots, the engine and the model store
//! (docs/design/std-ai-lifecycle.md §5.5).
//!
//! The slots are the ones the source names (`project::ai_slots`). For each,
//! the checks apply the runtime's own resolution rule to the operator's
//! environment, so a deploy that would fail at the first call fails here
//! first: a `MODE` that is not `local` or `remote`, `MODE=local` with a `URL`,
//! `MODE=remote` without one, a URL that is not `http(s)://`, a registry
//! adapter that names no alias, a remote target that does not answer. The
//! machine-wide settings are checked as the runtime reads them
//! (`AI_FETCH`, `AI_ACCEL`, `AI_LLAMA_BIN`, `AI_REGISTRY`), and the engine
//! and models a local slot needs must be provisioned when fetching is off.
//!
//! A finding never prints a variable's value beyond a URL's scheme, host and
//! port. The checks are pure over their inputs ([`Inputs`]) so they are
//! unit-tested here; `main.rs` gathers the inputs and prints the findings.

use std::path::{Path, PathBuf};
use std::time::Duration;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Level {
    Info,
    Warn,
    Error,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AiFinding {
    pub check: &'static str,
    pub level: Level,
    pub message: String,
    pub hint: String,
}

/// What the checks read.
pub struct Inputs<'a> {
    /// The runtime prefix (`SKY` unless `[env] prefix` changes it).
    pub prefix: String,
    /// The slots the source declares.
    pub slots: Vec<String>,
    /// An operator variable (an empty value reads as unset).
    pub env: &'a dyn Fn(&str) -> Option<String>,
    /// The AI home (engines, models, artefacts).
    pub home: PathBuf,
    /// Whether the app runs in production (`ENV` is set and not dev).
    pub production: bool,
    /// Probes a remote target (`host:port`); true when it accepts a connection.
    pub reachable: &'a dyn Fn(&str) -> bool,
}

impl Inputs<'_> {
    fn var(&self, suffix: &str) -> (String, Option<String>) {
        let name = format!("{}_{suffix}", self.prefix);
        let v = (self.env)(&name).filter(|v| !v.trim().is_empty());
        (name, v)
    }
}

fn finding(check: &'static str, level: Level, message: String, hint: &str) -> AiFinding {
    AiFinding {
        check,
        level,
        message,
        hint: hint.to_string(),
    }
}

/// `scheme://host:port` of a URL, never its user, path or query.
pub fn redact_url(u: &str) -> String {
    let Some((scheme, rest)) = u.split_once("://") else {
        return "(invalid URL)".into();
    };
    let host = rest.split(['/', '?', '#']).next().unwrap_or("");
    let host = host.rsplit('@').next().unwrap_or(host);
    format!("{scheme}://{host}")
}

/// `host:port` to probe for a URL (the scheme's default port when none).
fn probe_target(u: &str) -> Option<String> {
    let (scheme, rest) = u.split_once("://")?;
    let host = rest.split(['/', '?', '#']).next()?;
    let host = host.rsplit('@').next()?;
    if host.is_empty() {
        return None;
    }
    if host
        .rsplit_once(':')
        .is_some_and(|(_, p)| p.parse::<u16>().is_ok())
    {
        return Some(host.to_string());
    }
    let port = if scheme == "https" { 443 } else { 80 };
    Some(format!("{host}:{port}"))
}

/// Every Std.Ai finding for a project.
pub fn findings(i: &Inputs) -> Vec<AiFinding> {
    let mut out = Vec::new();
    let (fetch_name, fetch) = i.var("AI_FETCH");
    let fetch_on = match fetch.as_deref().map(|s| s.trim().to_ascii_lowercase()) {
        None => !i.production,
        Some(v) if ["on", "1", "true", "yes"].contains(&v.as_str()) => true,
        Some(v) if ["off", "0", "false", "no"].contains(&v.as_str()) => false,
        Some(_) => {
            out.push(finding(
                "ai-fetch-invalid",
                Level::Error,
                format!("{fetch_name} is neither on nor off"),
                "set it to on or off, or unset it (on in development, off in production)",
            ));
            !i.production
        }
    };
    let (accel_name, accel) = i.var("AI_ACCEL");
    if let Some(a) = accel {
        if !["cpu", "metal", "cuda", "rocm", "vulkan"]
            .contains(&a.trim().to_ascii_lowercase().as_str())
        {
            out.push(finding(
                "ai-accel-invalid",
                Level::Error,
                format!("{accel_name} is not cpu, metal, cuda, rocm or vulkan"),
                "set one of those, or unset it to detect the accelerator",
            ));
        }
    }
    let (reg_name, reg) = i.var("AI_REGISTRY");
    if let Some(r) = reg {
        let r = r.trim();
        let ok = ["file://", "gs://", "s3://"]
            .iter()
            .any(|p| r.starts_with(p) && r.len() > p.len());
        if !ok {
            out.push(finding(
                "ai-registry-invalid",
                Level::Error,
                format!("{reg_name} is not a file://, gs:// or s3:// location"),
                "write file://<dir>, gs://<bucket>/<prefix> or s3://<bucket>/<prefix>",
            ));
        }
    }

    let mut any_local = false;
    for slot in &i.slots {
        let key = |k: &str| i.var(&format!("AI_{}_{k}", slot.to_ascii_uppercase()));
        let (mode_name, mode) = key("MODE");
        let (url_name, url) = key("URL");
        let mode = mode.map(|m| m.trim().to_ascii_lowercase());
        if let Some(m) = &mode {
            if m != "local" && m != "remote" {
                out.push(finding(
                    "ai-slot-mode-invalid",
                    Level::Error,
                    format!("slot {slot}: {mode_name} is neither local nor remote"),
                    "set local or remote, or unset it to let the URL decide",
                ));
                continue;
            }
        }
        match (mode.as_deref(), &url) {
            (Some("local"), Some(_)) => {
                out.push(finding(
                    "ai-slot-conflict",
                    Level::Error,
                    format!("slot {slot}: {mode_name}=local and {url_name} is set; the slot fails at its first call"),
                    "unset the URL, or set the mode to remote",
                ));
                continue;
            }
            (Some("remote"), None) => {
                out.push(finding(
                    "ai-slot-no-url",
                    Level::Error,
                    format!("slot {slot}: {mode_name}=remote and {url_name} is not set"),
                    "set the URL to the OpenAI-compatible base URL (ending /v1)",
                ));
                continue;
            }
            _ => {}
        }
        if let Some(u) = &url {
            let u = u.trim();
            if !(u.starts_with("http://") || u.starts_with("https://")) {
                out.push(finding(
                    "ai-slot-url-invalid",
                    Level::Error,
                    format!("slot {slot}: {url_name} does not start with http:// or https://"),
                    "write the OpenAI-compatible base URL, for example https://gpu.internal:8443/v1",
                ));
            } else if let Some(target) = probe_target(u) {
                if !(i.reachable)(&target) {
                    out.push(finding(
                        "ai-slot-unreachable",
                        Level::Warn,
                        format!("slot {slot}: {} does not accept a connection from this machine", redact_url(u)),
                        "check the address and the network path; a remote slot never falls back to a local model",
                    ));
                }
            }
        } else {
            any_local = true;
        }
        let (adapter_name, adapter) = key("ADAPTER");
        if let Some(a) = adapter {
            if let Some(r) = a.trim().strip_prefix("registry:") {
                let ok = r
                    .split_once('@')
                    .is_some_and(|(n, al)| !n.is_empty() && !al.is_empty());
                if !ok {
                    out.push(finding(
                        "ai-slot-adapter-invalid",
                        Level::Error,
                        format!(
                            "slot {slot}: {adapter_name} is a registry reference without an alias"
                        ),
                        "write registry:<name>@<alias>, for example registry:support-lora@prod",
                    ));
                }
            }
        }
        if url.is_none() {
            let (model_name, model) = key("MODEL");
            if let Some(m) = model {
                if !m.contains("#sha256=") {
                    out.push(finding(
                        "ai-slot-model-unpinned",
                        Level::Error,
                        format!("slot {slot}: {model_name} is not pinned by a sha256 digest"),
                        "write hf:<org>/<repo>/<file>#sha256=<hex> or file:<path>#sha256=<hex> (`sky ai pull` prints one)",
                    ));
                } else if !fetch_on && m.starts_with("hf:") {
                    let sum = m.rsplit("#sha256=").next().unwrap_or("");
                    let stored = i.home.join("models/sha256").join(format!("{sum}.gguf"));
                    if !stored.is_file() {
                        out.push(finding(
                            "ai-model-not-provisioned",
                            Level::Error,
                            format!("slot {slot}: the model {model_name} names is not in the model store and fetching is off"),
                            "run `sky ai provision --model <the reference>` on this host first",
                        ));
                    }
                }
            }
        }
    }

    if any_local {
        let (bin_name, bin) = i.var("AI_LLAMA_BIN");
        let exe = if cfg!(windows) {
            "llama-server.exe"
        } else {
            "llama-server"
        };
        match bin {
            Some(dir) => {
                if !Path::new(dir.trim()).join(exe).is_file() {
                    out.push(finding(
                        "ai-engine-bin-incomplete",
                        Level::Error,
                        format!("{bin_name} has no {exe} in it"),
                        "point it at the directory that holds llama-server, or unset it to use the pinned build",
                    ));
                }
            }
            None => {
                let installed = std::fs::read_dir(i.home.join("engines/llama"))
                    .map(|rd| {
                        rd.filter_map(Result::ok)
                            .any(|e| e.path().join(exe).is_file())
                    })
                    .unwrap_or(false);
                if !installed {
                    if fetch_on {
                        out.push(finding(
                            "ai-engine-fetched-on-first-use",
                            Level::Info,
                            "the managed llama-server is not installed yet; a local slot downloads the pinned build on its first call".into(),
                            "run `sky ai provision` to install it ahead of time",
                        ));
                    } else {
                        out.push(finding(
                            "ai-engine-not-provisioned",
                            Level::Error,
                            format!("a local slot needs llama-server, it is not installed, and fetching is off ({fetch_name})"),
                            "run `sky ai provision` on this host first",
                        ));
                    }
                }
            }
        }
    }
    out
}

/// The AI home as the runtime resolves it: `<PREFIX>_AI_HOME`, else
/// `$SKY_HOME/ai`, else the user cache directory.
pub fn ai_home(prefix: &str, env: &dyn Fn(&str) -> Option<String>) -> PathBuf {
    if let Some(h) = env(&format!("{prefix}_AI_HOME")).filter(|v| !v.trim().is_empty()) {
        return PathBuf::from(h.trim());
    }
    if let Some(h) = env("SKY_HOME").filter(|v| !v.trim().is_empty()) {
        return PathBuf::from(h.trim()).join("ai");
    }
    let home = env("HOME").unwrap_or_default();
    if cfg!(target_os = "macos") {
        PathBuf::from(home).join("Library/Caches/sky/ai")
    } else if let Some(x) = env("XDG_CACHE_HOME").filter(|v| !v.is_empty()) {
        PathBuf::from(x).join("sky/ai")
    } else {
        PathBuf::from(home).join(".cache/sky/ai")
    }
}

/// A TCP connection to `host:port` within two seconds.
pub fn reachable(target: &str) -> bool {
    use std::net::ToSocketAddrs;
    let Ok(addrs) = target.to_socket_addrs() else {
        return false;
    };
    addrs
        .into_iter()
        .any(|a| std::net::TcpStream::connect_timeout(&a, Duration::from_secs(2)).is_ok())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn run(vars: &[(&str, &str)], slots: &[&str], production: bool, home: &Path) -> Vec<AiFinding> {
        let map: HashMap<String, String> = vars
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();
        let env = move |k: &str| map.get(k).cloned();
        let reach = |t: &str| t == "up.example:443";
        findings(&Inputs {
            prefix: "SKY".into(),
            slots: slots.iter().map(|s| s.to_string()).collect(),
            env: &env,
            home: home.to_path_buf(),
            production,
            reachable: &reach,
        })
    }

    fn checks(f: &[AiFinding]) -> Vec<&'static str> {
        f.iter().map(|x| x.check).collect()
    }

    fn empty_home() -> PathBuf {
        let d = std::env::temp_dir().join(format!("sky-ai-doctor-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn a_local_slot_with_a_url_is_the_runtime_conflict() {
        let f = run(
            &[
                ("SKY_AI_CHAT_MODE", "local"),
                ("SKY_AI_CHAT_URL", "https://x.example/v1"),
            ],
            &["chat"],
            false,
            &empty_home(),
        );
        assert!(checks(&f).contains(&"ai-slot-conflict"), "{f:?}");
    }

    #[test]
    fn a_remote_slot_needs_a_url_and_a_valid_one() {
        let f = run(
            &[("SKY_AI_CHAT_MODE", "remote")],
            &["chat"],
            false,
            &empty_home(),
        );
        assert_eq!(checks(&f), vec!["ai-slot-no-url"]);
        let f = run(
            &[("SKY_AI_CHAT_URL", "gpu.internal:8443")],
            &["chat"],
            false,
            &empty_home(),
        );
        assert_eq!(checks(&f), vec!["ai-slot-url-invalid"]);
        let f = run(
            &[("SKY_AI_CHAT_MODE", "cloud")],
            &["chat"],
            false,
            &empty_home(),
        );
        assert_eq!(checks(&f), vec!["ai-slot-mode-invalid"]);
    }

    #[test]
    fn an_unreachable_remote_is_a_warning_that_hides_the_url_path() {
        let f = run(
            &[(
                "SKY_AI_CHAT_URL",
                "https://user:pw@down.example/v1?key=s3cret",
            )],
            &["chat"],
            false,
            &empty_home(),
        );
        assert_eq!(checks(&f), vec!["ai-slot-unreachable"]);
        assert_eq!(f[0].level, Level::Warn);
        assert!(
            f[0].message.contains("https://down.example"),
            "{}",
            f[0].message
        );
        assert!(!f[0].message.contains("s3cret") && !f[0].message.contains("pw@"));
        let f = run(
            &[("SKY_AI_CHAT_URL", "https://up.example/v1")],
            &["chat"],
            false,
            &empty_home(),
        );
        assert!(f.is_empty(), "{f:?}");
    }

    #[test]
    fn a_registry_adapter_needs_an_alias() {
        let f = run(
            &[
                ("SKY_AI_CHAT_URL", "https://up.example/v1"),
                ("SKY_AI_CHAT_ADAPTER", "registry:support-lora"),
            ],
            &["chat"],
            false,
            &empty_home(),
        );
        assert_eq!(checks(&f), vec!["ai-slot-adapter-invalid"]);
    }

    #[test]
    fn production_without_an_engine_or_the_model_is_an_error() {
        let home = empty_home();
        let f = run(
            &[("SKY_AI_CHAT_MODEL", "hf:Qwen/R/f.gguf#sha256=abc")],
            &["chat"],
            true,
            &home,
        );
        let c = checks(&f);
        assert!(c.contains(&"ai-model-not-provisioned"), "{f:?}");
        assert!(c.contains(&"ai-engine-not-provisioned"), "{f:?}");
        // In development the engine is fetched on first use: information only.
        let f = run(&[], &["chat"], false, &home);
        assert_eq!(checks(&f), vec!["ai-engine-fetched-on-first-use"]);
        assert_eq!(f[0].level, Level::Info);
    }

    #[test]
    fn machine_settings_are_checked() {
        let f = run(
            &[
                ("SKY_AI_FETCH", "maybe"),
                ("SKY_AI_ACCEL", "tpu"),
                ("SKY_AI_REGISTRY", "ftp://x"),
            ],
            &[],
            false,
            &empty_home(),
        );
        assert_eq!(
            checks(&f),
            vec![
                "ai-fetch-invalid",
                "ai-accel-invalid",
                "ai-registry-invalid"
            ]
        );
    }

    #[test]
    fn an_operator_engine_dir_must_hold_llama_server() {
        let f = run(
            &[("SKY_AI_LLAMA_BIN", "/nonexistent/dir")],
            &["chat"],
            false,
            &empty_home(),
        );
        assert_eq!(checks(&f), vec!["ai-engine-bin-incomplete"]);
    }

    #[test]
    fn no_slots_and_no_settings_is_silent() {
        assert!(run(&[], &[], true, &empty_home()).is_empty());
    }
}
