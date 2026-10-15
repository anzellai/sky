//! go_get_memo.rs — skip the per-build `go get` of a project's Go FFI
//! modules when its result is already known.
//!
//! Every build rewrites `sky-out/go.mod` from the runtime's copy and then runs
//! one `go get <module>@<spec>` per Go FFI module the program uses, so the
//! `sky.toml` pin wins (`build::inject_ffi_deps`). Each is a separate `go`
//! process that loads the whole module graph: 11 s on examples/13-skyshop's
//! first build, and several seconds on every warm rebuild.
//!
//! When every fetched module is pinned to an EXACT version (a full semver tag
//! or pseudo-version), the `go.mod` and `go.sum` the `go get`s produce are a
//! function of their inputs: the `go.mod` before the first `go get`, the
//! ordered list of `module@version`s, and the Go toolchain and the settings
//! that change resolution. So the result is stored under a hash of exactly
//! those inputs ([`key`]) in `sky-out/.sky-gomod/<key>/` and copied back on
//! the next build instead of running `go get`.
//!
//! The memo is used only when it cannot be wrong:
//! * no `@latest`, branch, short commit or partial version (`v1.6`) anywhere
//!   in the list ([`exact_version`]) — a floating spec re-resolves on every
//!   build, as before;
//! * no Go path dependency (its own `go.mod` is an input outside the key);
//! * a result is stored only when every `go get` exited 0 AND the resulting
//!   `go.mod` requires each pinned module at its pin ([`requires_all`]).

use std::path::{Path, PathBuf};

/// The memo directory inside `sky-out/`.
pub const DIR: &str = ".sky-gomod";

/// How many results to keep per project (newest by mtime).
const KEEP: usize = 4;

/// The environment variables that change what `go get` resolves or writes.
const ENV_INPUTS: &[&str] = &[
    "GOFLAGS",
    "GOTOOLCHAIN",
    "GOPROXY",
    "GONOSUMDB",
    "GONOSUMCHECK",
    "GOPRIVATE",
    "GONOPROXY",
    "GOSUMDB",
    "GOINSECURE",
    "GOWORK",
    "GOEXPERIMENT",
];

/// `v<major>.<minor>.<patch>` with an optional `-pre` / `+build` (a release
/// tag or a pseudo-version): the one kind of spec `go get` cannot re-resolve.
pub fn exact_version(spec: &str) -> bool {
    let Some(rest) = spec.trim().strip_prefix('v') else {
        return false;
    };
    let core = rest.split(['-', '+']).next().unwrap_or("");
    let parts: Vec<&str> = core.split('.').collect();
    parts.len() == 3
        && parts
            .iter()
            .all(|p| !p.is_empty() && p.chars().all(|c| c.is_ascii_digit()))
}

/// The memo key: the starting `go.mod`, the ordered `module@version` list, the
/// Go version and the resolution settings.
pub fn key(
    go_mod: &str,
    pins: &[(String, String)],
    go_version: &str,
    env: &[(&str, String)],
) -> String {
    let mut h = crate::sha256::Sha256::new();
    let mut put = |label: &str, v: &str| {
        h.update(label.as_bytes());
        h.update(&(v.len() as u64).to_le_bytes());
        h.update(v.as_bytes());
    };
    put("go.mod", go_mod);
    for (m, v) in pins {
        put("pin", &format!("{m}@{v}"));
    }
    put("go", go_version);
    for (k, v) in env {
        put(k, v);
    }
    h.finish()[..16]
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

/// Whether `go_mod` requires each pinned `(path, version)` at exactly that
/// version: a require of `path` itself, or of the module `path` lives in (a
/// sky.toml key may name a package, `firebase.google.com/go/v4/auth`).
pub fn requires_all(go_mod: &str, pins: &[(String, String)]) -> bool {
    pins.iter().all(|(p, v)| {
        go_mod.lines().any(|l| {
            let mut w = l.trim().trim_start_matches("require").split_whitespace();
            let (Some(m), Some(ver)) = (w.next(), w.next()) else {
                return false;
            };
            ver == v && (p == m || p.starts_with(&format!("{m}/")))
        })
    })
}

/// The resolution settings of this process, in a fixed order.
pub fn env_inputs() -> Vec<(&'static str, String)> {
    ENV_INPUTS
        .iter()
        .map(|k| (*k, std::env::var(k).unwrap_or_default()))
        .collect()
}

/// `go env GOVERSION`, or `None` when `go` cannot say (then no memo).
pub fn go_version() -> Option<String> {
    let o = std::process::Command::new("go")
        .args(["env", "GOVERSION"])
        .output()
        .ok()?;
    let v = String::from_utf8_lossy(&o.stdout).trim().to_string();
    (o.status.success() && !v.is_empty()).then_some(v)
}

fn entry(out_dir: &Path, key: &str) -> PathBuf {
    out_dir.join(DIR).join(key)
}

/// Copy a stored result into `out_dir`. `true` when it was there and copied.
pub fn restore(out_dir: &Path, key: &str) -> bool {
    let e = entry(out_dir, key);
    let (m, s) = (e.join("go.mod"), e.join("go.sum"));
    if !m.is_file() || !s.is_file() {
        return false;
    }
    let ok = std::fs::copy(&m, out_dir.join("go.mod")).is_ok()
        && std::fs::copy(&s, out_dir.join("go.sum")).is_ok();
    if ok {
        // Touch so pruning keeps what is in use.
        let _ = std::fs::File::options()
            .append(true)
            .open(&m)
            .and_then(|f| f.set_modified(std::time::SystemTime::now()));
    }
    ok
}

/// Store `out_dir`'s `go.mod` / `go.sum` under `key` (temp dir + rename), and
/// keep only the newest [`KEEP`] results. Best-effort.
pub fn store(out_dir: &Path, key: &str) {
    let root = out_dir.join(DIR);
    let tmp = root.join(format!(".tmp-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&tmp);
    let staged = std::fs::create_dir_all(&tmp).is_ok()
        && std::fs::copy(out_dir.join("go.mod"), tmp.join("go.mod")).is_ok()
        && std::fs::copy(out_dir.join("go.sum"), tmp.join("go.sum")).is_ok();
    let dest = entry(out_dir, key);
    if !staged || std::fs::rename(&tmp, &dest).is_err() {
        let _ = std::fs::remove_dir_all(&tmp);
        return;
    }
    prune(&root);
}

fn prune(root: &Path) {
    let Ok(rd) = std::fs::read_dir(root) else {
        return;
    };
    let mut v: Vec<(std::time::SystemTime, PathBuf)> = rd
        .flatten()
        .filter(|e| !e.file_name().to_string_lossy().starts_with('.'))
        .filter_map(|e| {
            let t = std::fs::metadata(e.path().join("go.mod"))
                .ok()?
                .modified()
                .ok()?;
            Some((t, e.path()))
        })
        .collect();
    v.sort_by(|a, b| b.0.cmp(&a.0));
    for (_, p) in v.into_iter().skip(KEEP) {
        let _ = std::fs::remove_dir_all(p);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pins(v: &[(&str, &str)]) -> Vec<(String, String)> {
        v.iter()
            .map(|(a, b)| (a.to_string(), b.to_string()))
            .collect()
    }

    #[test]
    fn only_a_full_version_is_exact() {
        for ok in [
            "v1.6.0",
            "v84.4.1",
            "v2.0.0-rc1",
            "v0.0.0-20240107210532-573471604cb6",
            "v2.0.0+incompatible",
        ] {
            assert!(exact_version(ok), "{ok}");
        }
        for floating in [
            "latest", "", "main", "v1.6", "v1", "abc1234", "1.6.0", "v1.6.x",
        ] {
            assert!(!exact_version(floating), "{floating}");
        }
    }

    #[test]
    fn the_key_covers_every_input() {
        let env = vec![("GOFLAGS", String::new()), ("GOTOOLCHAIN", String::new())];
        let p = pins(&[("a.io/x", "v1.0.0"), ("b.io/y", "v2.0.0")]);
        let k = key("module m\n", &p, "go1.26.1", &env);
        assert_eq!(k, key("module m\n", &p, "go1.26.1", &env), "deterministic");
        assert_ne!(k, key("module m\nrequire z v1\n", &p, "go1.26.1", &env));
        assert_ne!(
            k,
            key(
                "module m\n",
                &pins(&[("a.io/x", "v1.0.1"), ("b.io/y", "v2.0.0")]),
                "go1.26.1",
                &env
            )
        );
        assert_ne!(
            k,
            key(
                "module m\n",
                &pins(&[("b.io/y", "v2.0.0"), ("a.io/x", "v1.0.0")]),
                "go1.26.1",
                &env
            ),
            "order"
        );
        assert_ne!(k, key("module m\n", &p, "go1.26.2", &env));
        let env2 = vec![
            ("GOFLAGS", "-mod=vendor".to_string()),
            ("GOTOOLCHAIN", String::new()),
        ];
        assert_ne!(k, key("module m\n", &p, "go1.26.1", &env2));
    }

    #[test]
    fn a_result_counts_only_when_go_mod_requires_every_pin() {
        let gm = "module sky-app\n\ngo 1.25.0\n\nrequire (\n\ta.io/x v1.0.0\n\tb.io/y v2.0.0 // indirect\n)\n\nrequire c.io/z v0.1.0\n";
        assert!(requires_all(
            gm,
            &pins(&[
                ("a.io/x", "v1.0.0"),
                ("b.io/y", "v2.0.0"),
                ("c.io/z", "v0.1.0")
            ])
        ));
        assert!(!requires_all(gm, &pins(&[("a.io/x", "v1.0.1")])));
        assert!(!requires_all(gm, &pins(&[("d.io/w", "v1.0.0")])));
        // A package under a required module, at the module's version.
        assert!(requires_all(gm, &pins(&[("a.io/x/sub", "v1.0.0")])));
        assert!(!requires_all(gm, &pins(&[("a.io/xy", "v1.0.0")])));
    }

    #[test]
    fn a_stored_result_is_restored_and_old_ones_are_pruned() {
        let d = std::env::temp_dir().join(format!("sky-gogetmemo-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        assert!(!restore(&d, "k0"));
        for i in 0..6 {
            std::fs::write(d.join("go.mod"), format!("module m{i}\n")).unwrap();
            std::fs::write(d.join("go.sum"), format!("sum{i}\n")).unwrap();
            store(&d, &format!("k{i}"));
            let t = std::time::SystemTime::UNIX_EPOCH + std::time::Duration::from_secs(1000 + i);
            std::fs::File::options()
                .append(true)
                .open(d.join(DIR).join(format!("k{i}")).join("go.mod"))
                .unwrap()
                .set_modified(t)
                .unwrap();
        }
        std::fs::write(d.join("go.mod"), "changed").unwrap();
        assert!(restore(&d, "k5"));
        assert_eq!(
            std::fs::read_to_string(d.join("go.mod")).unwrap(),
            "module m5\n"
        );
        assert_eq!(std::fs::read_to_string(d.join("go.sum")).unwrap(), "sum5\n");
        // A store prunes to the newest KEEP; k0 was the oldest when k5 was stored.
        assert!(!restore(&d, "k0"));
        let _ = std::fs::remove_dir_all(&d);
    }
}
