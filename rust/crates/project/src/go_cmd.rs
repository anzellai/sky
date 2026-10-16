//! go_cmd.rs — the one place `sky` makes a `go build` command.
//!
//! Every Go compile `sky` runs goes through [`GoBuild::command`]: the native
//! app build (static first, then the cgo retry), the Sky.Spa wasm client, the
//! cache warm-up ([`crate::go_cache::prime`]), the Sky Console hub, and every
//! child `sky build` (the Sky.Spa legs, a Std.App entry, a desktop shell). One
//! builder means one set of flags, so a package compiled by one of them is a
//! cache hit for the others.
//!
//! `-trimpath`. Without it Go records the absolute source directory of every
//! package it compiles, and that directory is part of the package's build
//! cache key. The same `sky-app/rt` in `~/a/sky-out` and `~/b/sky-out` then has
//! two keys, so every new project directory, every Sky.Spa leg, every desktop
//! shell and every CI checkout path recompiled the whole runtime. With
//! `-trimpath` the key depends on the package's content and import path only.
//!
//! What it changes in a built app: a panic stack trace names files by module
//! path (`sky-app/main.go`, `sky-app/rt/live.go`) instead of the absolute path
//! on the build machine. Function names are unchanged. `go build` errors still
//! name `./main.go:LINE:COL`, so Sky's diagnostics mapping is unchanged.
//!
//! Opt-out: `sky build --no-trimpath` (or `SKY_GO_TRIMPATH=0`). `sky run
//! --profile` turns it off for its build, because `pprof -list` / `pprof
//! -http` source views and Delve read the recorded absolute source paths.

use std::ffi::{OsStr, OsString};
use std::path::Path;
use std::process::Command;

/// `SKY_GO_TRIMPATH=0` (or `false` / `no` / `off`) builds without `-trimpath`.
/// Unset or any other value keeps it on. `sky build --no-trimpath` sets it.
pub const ENV_TRIMPATH: &str = "SKY_GO_TRIMPATH";

/// What a `go build` compiles for.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Target {
    /// The host platform. `cgo` sets `CGO_ENABLED`.
    Native { cgo: bool },
    /// `GOOS=js GOARCH=wasm` (a Sky.Spa client). Links with `-ldflags=-s -w`
    /// when it produces an output file.
    Wasm,
}

/// One `go build` invocation.
#[derive(Debug, Clone)]
pub struct GoBuild<'a> {
    /// The working directory (the module root of the emitted tree).
    pub dir: &'a Path,
    pub target: Target,
    /// `-p=N` from [`crate::go_jobs`], or `None` for Go's default.
    pub jobs: Option<&'a str>,
    /// `-o <output>`; `None` builds without writing a binary (`./rt/...`).
    pub output: Option<&'a OsStr>,
    /// The package patterns (`.` or `./rt/...`).
    pub packages: &'a [&'a str],
    /// Extra build flags for a diagnostic run (`-x`, `-debug-trace=…`). Empty
    /// for every normal build.
    pub extra: &'a [&'a str],
}

/// Whether `-trimpath` is on, from the value of [`ENV_TRIMPATH`].
pub fn trimpath_from(value: Option<&str>) -> bool {
    !matches!(
        value.map(|v| v.trim().to_ascii_lowercase()).as_deref(),
        Some("0" | "false" | "no" | "off")
    )
}

/// Whether `-trimpath` is on for this process.
pub fn trimpath() -> bool {
    trimpath_from(std::env::var(ENV_TRIMPATH).ok().as_deref())
}

/// `GOFLAGS` for `go build`. Preserves whatever the user set (so a container's
/// `-buildvcs=false` survives — the old hard-coded `GOFLAGS=-mod=mod` OVERWROTE
/// the env, so Go then tried to VCS-stamp a Go-workspace synthetic parent repo
/// and failed), then forces the two Sky always needs: `-mod=mod` (Sky manages
/// the emitted `go.mod`) and `-buildvcs=false` (the emitted Go is generated —
/// there is never a source revision to stamp, and stamping breaks inside odd
/// git/workspace boundaries). Sky's values replace any conflicting user `-mod=`
/// / `-buildvcs=`; every other user flag is kept.
pub fn goflags_from(existing: &str) -> String {
    let mut flags: Vec<String> = existing
        .split_whitespace()
        .filter(|f| !f.starts_with("-mod=") && !f.starts_with("-buildvcs="))
        .map(String::from)
        .collect();
    flags.push("-mod=mod".to_string());
    flags.push("-buildvcs=false".to_string());
    flags.join(" ")
}

/// [`goflags_from`] over this process's `GOFLAGS`.
pub fn goflags() -> String {
    goflags_from(&std::env::var("GOFLAGS").unwrap_or_default())
}

/// `GOFLAGS` for a build of a module Sky does NOT generate (the Sky Console
/// hub, built from the runtime's own `runtime-go/` module): the user's flags
/// with `-mod=readonly`, so the build never rewrites that module's `go.mod` /
/// `go.sum` (in a source checkout they are the repository's own files), and
/// `-buildvcs=false` as for every Sky build.
pub fn goflags_readonly_from(existing: &str) -> String {
    let mut flags: Vec<String> = existing
        .split_whitespace()
        .filter(|f| !f.starts_with("-mod=") && !f.starts_with("-buildvcs="))
        .map(String::from)
        .collect();
    flags.push("-mod=readonly".to_string());
    flags.push("-buildvcs=false".to_string());
    flags.join(" ")
}

impl GoBuild<'_> {
    /// The argv after `go`, for a given `-trimpath` setting. Pure.
    pub fn args(&self, trimpath: bool) -> Vec<OsString> {
        let mut a: Vec<OsString> = vec!["build".into()];
        if let Some(j) = self.jobs {
            a.push(j.into());
        }
        if trimpath {
            a.push("-trimpath".into());
        }
        a.extend(self.extra.iter().map(OsString::from));
        if let Some(out) = self.output {
            // `-s -w` strips the symbol table and DWARF. A browser reads
            // neither, and for GOARCH=wasm they are pure download weight. One
            // argv element, so `-s -w` is the flag's value.
            if self.target == Target::Wasm {
                a.push("-ldflags=-s -w".into());
            }
            a.push("-o".into());
            a.push(out.to_os_string());
        }
        a.extend(self.packages.iter().map(OsString::from));
        a
    }

    /// The environment Sky sets on top of the inherited one. Pure.
    pub fn env(&self, goflags: &str) -> Vec<(&'static str, String)> {
        let mut e = vec![("GOFLAGS", goflags.to_string())];
        match self.target {
            Target::Native { cgo } => e.push(("CGO_ENABLED", if cgo { "1" } else { "0" }.into())),
            Target::Wasm => {
                e.push(("GOOS", "js".into()));
                e.push(("GOARCH", "wasm".into()));
                e.push(("CGO_ENABLED", "0".into()));
            }
        }
        e
    }

    /// The command, ready to run: argv, environment, and Sky's Go build cache
    /// (`go_cache::apply`). The caller holds `go_cache::hold_shared()` while it
    /// runs.
    pub fn command(&self) -> Command {
        self.command_with_goflags(&goflags())
    }

    /// [`Self::command`] for a module Sky does not generate: `go.mod` /
    /// `go.sum` stay read-only ([`goflags_readonly_from`]).
    pub fn command_readonly(&self) -> Command {
        self.command_with_goflags(&goflags_readonly_from(
            &std::env::var("GOFLAGS").unwrap_or_default(),
        ))
    }

    fn command_with_goflags(&self, goflags: &str) -> Command {
        let mut cmd = Command::new("go");
        cmd.current_dir(self.dir).args(self.args(trimpath()));
        for (k, v) in self.env(goflags) {
            cmd.env(k, v);
        }
        crate::go_cache::apply(&mut cmd);
        cmd
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn strs(v: &[OsString]) -> Vec<String> {
        v.iter().map(|s| s.to_string_lossy().into_owned()).collect()
    }

    #[test]
    fn a_native_build_trims_paths_and_names_its_output() {
        let b = GoBuild {
            dir: Path::new("/x"),
            target: Target::Native { cgo: false },
            jobs: Some("-p=2"),
            output: Some(OsStr::new("app")),
            packages: &["."],
            extra: &[],
        };
        assert_eq!(
            strs(&b.args(true)),
            ["build", "-p=2", "-trimpath", "-o", "app", "."]
        );
        assert_eq!(strs(&b.args(false)), ["build", "-p=2", "-o", "app", "."]);
        assert_eq!(
            b.env("-mod=mod -buildvcs=false"),
            vec![
                ("GOFLAGS", "-mod=mod -buildvcs=false".to_string()),
                ("CGO_ENABLED", "0".to_string())
            ]
        );
        let cgo = GoBuild {
            target: Target::Native { cgo: true },
            ..b
        };
        assert_eq!(cgo.env("")[1], ("CGO_ENABLED", "1".to_string()));
    }

    #[test]
    fn a_wasm_build_strips_and_targets_js() {
        let b = GoBuild {
            dir: Path::new("/x"),
            target: Target::Wasm,
            jobs: None,
            output: Some(OsStr::new("main.wasm")),
            packages: &["."],
            extra: &[],
        };
        assert_eq!(
            strs(&b.args(true)),
            [
                "build",
                "-trimpath",
                "-ldflags=-s -w",
                "-o",
                "main.wasm",
                "."
            ]
        );
        let env = b.env("g");
        assert!(env.contains(&("GOOS", "js".to_string())), "{env:?}");
        assert!(env.contains(&("GOARCH", "wasm".to_string())), "{env:?}");
    }

    /// The cache warm-up compiles packages without linking: no `-o`, no
    /// `-ldflags`, but the same `-trimpath` and `GOFLAGS` as a project build,
    /// so its objects are the project's cache hits.
    #[test]
    fn a_package_build_has_the_project_flags_without_an_output() {
        for target in [Target::Native { cgo: false }, Target::Wasm] {
            let b = GoBuild {
                dir: Path::new("/x"),
                target,
                jobs: None,
                output: None,
                packages: &["./rt/..."],
                extra: &[],
            };
            assert_eq!(strs(&b.args(true)), ["build", "-trimpath", "./rt/..."]);
        }
    }

    #[test]
    fn the_trimpath_switch_reads_off_values_only() {
        for v in [None, Some(""), Some("1"), Some("yes"), Some("on")] {
            assert!(trimpath_from(v), "{v:?}");
        }
        for v in ["0", "false", "NO", " off "] {
            assert!(!trimpath_from(Some(v)), "{v}");
        }
    }

    /// N7: a module Sky does not generate is built read-only. `-mod=mod`
    /// there let `go build` rewrite the runtime's own go.mod / go.sum.
    #[test]
    fn a_module_sky_does_not_generate_is_built_read_only() {
        assert_eq!(goflags_readonly_from(""), "-mod=readonly -buildvcs=false");
        assert_eq!(
            goflags_readonly_from("-mod=mod -tags=x"),
            "-tags=x -mod=readonly -buildvcs=false"
        );
        let b = GoBuild {
            dir: Path::new("/x"),
            target: Target::Native { cgo: false },
            jobs: None,
            output: Some(OsStr::new("hub")),
            packages: &["./cmd/sky-hub"],
            extra: &[],
        };
        let flags = b
            .command_readonly()
            .get_envs()
            .find(|(k, _)| *k == "GOFLAGS")
            .and_then(|(_, v)| v.map(|v| v.to_string_lossy().into_owned()))
            .unwrap();
        assert!(flags.contains("-mod=readonly"), "{flags}");
        assert!(!flags.contains("-mod=mod"), "{flags}");
    }

    #[test]
    fn goflags_keep_user_flags_and_force_skys_two() {
        assert_eq!(goflags_from(""), "-mod=mod -buildvcs=false");
        assert_eq!(
            goflags_from("-mod=vendor -tags=x -buildvcs=true"),
            "-tags=x -mod=mod -buildvcs=false"
        );
    }
}
