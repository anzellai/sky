//! Tool installation metadata only: never ask the Go command to print settings.
//! The printed compiler path is data, not a shell command to parse or execute.
use std::ffi::OsStr;
use std::path::{Path, PathBuf};
use std::process::Command;

fn one_line(bytes: &[u8]) -> Option<&str> {
    let text = std::str::from_utf8(bytes).ok()?;
    let line = text
        .strip_suffix("\r\n")
        .or_else(|| text.strip_suffix('\n'))
        .unwrap_or(text);
    (!line.is_empty() && !line.contains(['\n', '\r', '\0'])).then_some(line)
}

fn parse_version(bytes: &[u8]) -> Option<String> {
    let body = one_line(bytes)?.strip_prefix("go version ")?;
    let (version, host) = body.rsplit_once(' ')?;
    if version.is_empty() || version.trim() != version {
        return None;
    }
    let (os, arch) = host.split_once('/')?;
    if !host_word(os) || !host_word(arch) {
        return None;
    }
    let release = version
        .strip_prefix("go")
        .is_some_and(|s| s.as_bytes().first().is_some_and(u8::is_ascii_digit));
    if !release && version != "devel" && !version.starts_with("devel ") {
        return None;
    }
    Some(version.to_owned())
}

fn host_word(word: &str) -> bool {
    !word.is_empty()
        && word
            .bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit())
}

pub(crate) fn version_using(go: &OsStr, dir: Option<&Path>) -> Option<String> {
    let mut command = Command::new(go);
    command.arg("version").stderr(std::process::Stdio::null());
    if let Some(dir) = dir {
        command.current_dir(dir);
    }
    let output = command.output().ok()?;
    output.status.success().then_some(())?;
    parse_version(&output.stdout)
}

/// Go1.26 prints strings.Join([toolPath], " ") for this exact argument list:
/// one RAW path, including spaces/quotes literally, followed by one newline.
/// Never split whitespace, strip shell quotes, or execute any returned text.
fn root_from_compile_output(bytes: &[u8]) -> Option<PathBuf> {
    let compiler = PathBuf::from(one_line(bytes)?);
    if !compiler.is_absolute()
        || !matches!(compiler.file_name()?.to_str()?, "compile" | "compile.exe")
    {
        return None;
    }
    let host = compiler.parent()?;
    let (os, arch) = host.file_name()?.to_str()?.split_once('_')?;
    if !host_word(os) || !host_word(arch) {
        return None;
    }
    let tool = host.parent()?;
    let pkg = tool.parent()?;
    if tool.file_name()? != "tool" || pkg.file_name()? != "pkg" {
        return None;
    }
    Some(pkg.parent()?.to_path_buf())
}

pub(crate) fn wasm_exec_using(go: &OsStr, dir: &Path) -> Result<PathBuf, &'static str> {
    let output = Command::new(go)
        .args(["tool", "-n", "compile"])
        .stderr(std::process::Stdio::null())
        .current_dir(dir)
        .output()
        .map_err(|_| "could not query Go compiler installation metadata")?;
    if !output.status.success() {
        return Err("Go compiler installation metadata query failed");
    }
    let root = root_from_compile_output(&output.stdout)
        .ok_or("Go compiler metadata was not a single absolute pkg/tool/host/compile path")?;
    let compiler = PathBuf::from(one_line(&output.stdout).ok_or("invalid Go compiler metadata")?);
    // Keep the logical SDK path: canonicalising a Nix/Homebrew compiler symlink
    // can move away from the installation containing the matching wasm script.
    if !compiler.is_file() || !root.is_dir() {
        return Err("Go compiler installation metadata path does not exist");
    }
    [
        root.join("lib/wasm/wasm_exec.js"),
        root.join("misc/wasm/wasm_exec.js"),
    ]
    .into_iter()
    .find(|path| path.is_file())
    .ok_or("matching Go installation has no lib/wasm or misc/wasm execution script")
}

#[cfg(all(test, unix))]
mod fake_tool_tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;
    use std::sync::atomic::{AtomicUsize, Ordering};

    static NEXT: AtomicUsize = AtomicUsize::new(0);
    pub(super) struct Fixture {
        pub(super) root: PathBuf,
        pub(super) work: PathBuf,
        pub(super) go: PathBuf,
        pub(super) compiler: PathBuf,
        pub(super) script: PathBuf,
    }
    impl Drop for Fixture {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.root);
        }
    }
    fn quoted(s: &str) -> String {
        format!("'{}'", s.replace('\'', "'\\''"))
    }
    pub(super) fn fixture(version: &str, tool_output: Option<&str>, legacy: bool) -> Fixture {
        let serial = NEXT.fetch_add(1, Ordering::Relaxed);
        let stamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let root = PathBuf::from("/tmp").join(format!(
            "sky-go-metadata-public-{}-{stamp}-{serial}",
            std::process::id()
        ));
        std::fs::create_dir(&root).unwrap();
        let work = root.join("module");
        std::fs::create_dir(&work).unwrap();
        std::fs::write(work.join("selected-toolchain.marker"), "public").unwrap();
        let sdk = root.join("SDK spaces 'literal' $(printf not-executed)");
        let compiler = sdk.join("pkg/tool/darwin_arm64/compile");
        std::fs::create_dir_all(compiler.parent().unwrap()).unwrap();
        // Not a real compiler: discovery must stat it, never execute it.
        std::fs::write(
            &compiler,
            format!(
                "#!/bin/sh\n: > {}\nexit 93\n",
                quoted(root.join("compiler-executed.marker").to_str().unwrap())
            ),
        )
        .unwrap();
        std::fs::set_permissions(&compiler, std::fs::Permissions::from_mode(0o755)).unwrap();
        let script = sdk.join(if legacy {
            "misc/wasm/wasm_exec.js"
        } else {
            "lib/wasm/wasm_exec.js"
        });
        std::fs::create_dir_all(script.parent().unwrap()).unwrap();
        std::fs::write(&script, "public selected runtime").unwrap();
        let go = root.join("fake go");
        let tool = tool_output
            .map(str::to_owned)
            .unwrap_or_else(|| compiler.to_str().unwrap().to_owned());
        let text = format!("#!/bin/sh\ncase \"$1\" in\n env) : > {}; exit 91;;\n version) [ -f selected-toolchain.marker ] || exit 92; printf '%s\\n' {}; exit 0;;\n tool) [ \"$#\" -eq 3 ] && [ \"$2\" = -n ] && [ \"$3\" = compile ] && [ -f selected-toolchain.marker ] || exit 92; printf '%s\\n' {}; exit 0;;\n *) : > {}; exit 94;;\nesac\n",
            quoted(root.join("env-refused.marker").to_str().unwrap()), quoted(version), quoted(&tool), quoted(root.join("unexpected-command.marker").to_str().unwrap()));
        std::fs::write(&go, text).unwrap();
        std::fs::set_permissions(&go, std::fs::Permissions::from_mode(0o755)).unwrap();
        Fixture {
            root,
            work,
            go,
            compiler,
            script,
        }
    }

    #[test]
    fn fake_go_version_never_requests_a_setting_dump_and_preserves_devel_body() {
        let f = fixture(
            "go version devel go1.27-public 2026-11-04 X:public-experiment darwin/arm64",
            None,
            false,
        );
        let result = version_using(f.go.as_os_str(), Some(&f.work));
        assert!(
            !f.root.join("env-refused.marker").exists(),
            "probe attempted a forbidden settings command"
        );
        assert_eq!(
            result.as_deref(),
            Some("devel go1.27-public 2026-11-04 X:public-experiment")
        );
        assert!(!f.root.join("unexpected-command.marker").exists());
    }

    #[test]
    fn fake_go_sdk_uses_dry_run_selected_installation_and_never_executes_its_path() {
        for legacy in [false, true] {
            let f = fixture("go version go1.26.10 darwin/arm64", None, legacy);
            let result = wasm_exec_using(f.go.as_os_str(), &f.work);
            assert!(
                !f.root.join("env-refused.marker").exists(),
                "probe attempted a forbidden settings command"
            );
            assert_eq!(result.unwrap(), f.script);
            assert!(!f.root.join("compiler-executed.marker").exists());
            assert!(!f.root.join("unexpected-command.marker").exists());
        }
    }

    #[test]
    fn malformed_fake_metadata_is_not_memoized_or_treated_as_a_command() {
        let f = fixture(
            "go version missing-host",
            Some("/not/a/pkg/tool/host/compile --execute-this"),
            false,
        );
        assert!(version_using(f.go.as_os_str(), Some(&f.work)).is_none());
        assert!(wasm_exec_using(f.go.as_os_str(), &f.work).is_err());
    }
}

#[cfg(test)]
mod parser_tests {
    use super::*;
    #[test]
    fn version_framing_preserves_release_devel_and_experiment_metadata_exactly() {
        for (raw, expected) in [
            ("go version go1.26.10 darwin/arm64\n", "go1.26.10"),
            (
                "go version go1.27rc1 X:public,feature windows/amd64\r\n",
                "go1.27rc1 X:public,feature",
            ),
            (
                "go version devel go1.27-public 2026-11-04 X:public linux/amd64\n",
                "devel go1.27-public 2026-11-04 X:public",
            ),
            ("go version devel linux/arm64", "devel"),
        ] {
            assert_eq!(parse_version(raw.as_bytes()).as_deref(), Some(expected));
        }
        for raw in [
            "",
            "go1.26.10",
            "go version go1.26.10",
            "go version unknown linux/amd64",
            "go version go1.26.10 linux/",
            "go version go1.26.10 /amd64",
            "go version go1.26.10 linux/amd64 extra",
            "go version go1.26.10  linux/amd64",
            "go version devel  linux/amd64",
            "go version go1.26.10 linux/amd64\nextra",
            "go version go1.26.10 linux/amd64\n\n",
        ] {
            assert!(parse_version(raw.as_bytes()).is_none());
        }
        assert!(parse_version(&[0xff]).is_none());
    }
    #[test]
    fn compiler_path_is_one_raw_path_not_shell_quoted_or_argument_split() {
        #[cfg(unix)]
        {
            for root in [
                "/nix/store/public-go/share/go",
                "/opt/homebrew/Cellar/go/1.26.10/libexec",
                "/public cache/golang.org/toolchain@v0.0.1-go1.26.10.darwin-arm64",
                "/SDK spaces 'literal' $(printf no-exec)",
            ] {
                let raw = format!("{root}/pkg/tool/darwin_arm64/compile\n");
                assert_eq!(
                    root_from_compile_output(raw.as_bytes()),
                    Some(PathBuf::from(root))
                );
            }
            for raw in [
                "relative/pkg/tool/darwin_arm64/compile",
                "/sdk/bin/compile",
                "/sdk/pkg/tool/darwin/compile",
                "/sdk/pkg/tool/darwin_arm64/compile --flags",
                "\"/sdk/pkg/tool/darwin_arm64/compile\"",
                "/sdk/pkg/tool/darwin_arm64/compile\nextra",
            ] {
                assert!(root_from_compile_output(raw.as_bytes()).is_none());
            }
        }
        #[cfg(windows)]
        {
            for root in [r"C:\Program Files\Go", r"\\server\share\Go SDK"] {
                let raw = format!("{root}\\pkg\\tool\\windows_amd64\\compile.exe\r\n");
                assert_eq!(
                    root_from_compile_output(raw.as_bytes()),
                    Some(PathBuf::from(root))
                );
            }
        }
        assert!(root_from_compile_output(&[0xff]).is_none());
    }
}

#[cfg(all(test, unix))]
mod sdk_filesystem_tests {
    use super::*;
    use std::os::unix::fs::symlink;
    #[test]
    fn sdk_discovery_keeps_logical_symlink_root_and_prefers_current_layout() {
        // The fake fixture's logical compiler path is different from its real
        // target, just as a packaged compiler symlink may leave its SDK root.
        let f = super::fake_tool_tests::fixture("go version go1.26.10 darwin/arm64", None, true);
        std::fs::remove_file(&f.compiler).unwrap();
        let target = f.root.join("compiler elsewhere");
        std::fs::write(&target, "public compiler metadata only").unwrap();
        symlink(&target, &f.compiler).unwrap();
        assert_eq!(
            wasm_exec_using(f.go.as_os_str(), &f.work).unwrap(),
            f.script
        );
        let root = f
            .compiler
            .parent()
            .unwrap()
            .parent()
            .unwrap()
            .parent()
            .unwrap()
            .parent()
            .unwrap();
        let current = root.join("lib/wasm/wasm_exec.js");
        std::fs::create_dir_all(current.parent().unwrap()).unwrap();
        std::fs::write(&current, "current layout").unwrap();
        assert_eq!(wasm_exec_using(f.go.as_os_str(), &f.work).unwrap(), current);
        std::fs::remove_file(&f.compiler).unwrap();
        assert!(wasm_exec_using(f.go.as_os_str(), &f.work).is_err());
    }
}
