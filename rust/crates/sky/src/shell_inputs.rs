//! Incremental builds of the mobile shells (`mobile:ios`, `mobile:android`
//! and the tablet targets that share them).
//!
//! A shell build used to start with `rm -rf build` and run swiftc, or aapt2,
//! javac, d8, zipalign and apksigner, on every `sky build`, although the shell
//! only changes when its generated sources, its bundle settings, its signing
//! or the toolchain change (the app itself is loaded from the backend). Now the
//! build hashes every input of the tool pipeline into one digest and records it
//! next to the artefact once the whole pipeline has succeeded. A later build
//! with the same digest, whose artefact is still in place, runs no tool at all.
//! Any other digest runs the full pipeline from a clean `build/` directory, as
//! before. The inputs are hashed by content, never by mtime, so a file that is
//! rewritten with the same bytes changes nothing and a file whose bytes change
//! always rebuilds. The toolchain is identified by the files that pin its
//! version (Xcode's `version.plist`, the SDK settings, the build-tools'
//! `source.properties`, the JDK's `release`) and by the path, size and mtime of
//! each tool binary, so identifying it runs no compiler.
//!
//! `sky package --release` does not use this: its scripts always build from a
//! clean `release/` directory.

use project::sha256::Sha256;
use std::collections::BTreeSet;
use std::ffi::{OsStr, OsString};
use std::path::{Path, PathBuf};
use std::process::Command;

/// The environment the shell's tools run in. Production builds use the
/// process environment unchanged; a test overrides `PATH`, `HOME`,
/// `ANDROID_HOME` or `DEVELOPER_DIR` to point the build at shims.
#[derive(Clone, Default)]
pub struct ToolEnv {
    overrides: Vec<(String, OsString)>,
}

impl ToolEnv {
    /// The process environment, unchanged.
    pub fn process() -> Self {
        Self::default()
    }

    /// This environment with `key` set to `value`.
    #[cfg(test)]
    pub fn with(mut self, key: &str, value: impl Into<OsString>) -> Self {
        self.overrides.push((key.to_string(), value.into()));
        self
    }

    /// The value of `key`: an override, else the process environment.
    pub fn var(&self, key: &str) -> Option<OsString> {
        self.overrides
            .iter()
            .rev()
            .find(|(k, _)| k == key)
            .map(|(_, v)| v.clone())
            .or_else(|| std::env::var_os(key))
    }

    /// A command for `program` with the overrides applied. A `PATH` override
    /// also decides where `program` is looked up.
    pub fn command(&self, program: impl AsRef<OsStr>) -> Command {
        let mut c = Command::new(program);
        for (k, v) in &self.overrides {
            c.env(k, v);
        }
        c
    }

    /// The first executable file named `name` on this environment's `PATH`.
    pub fn which(&self, name: &str) -> Option<PathBuf> {
        let path = self.var("PATH")?;
        std::env::split_paths(&path)
            .map(|d| d.join(name))
            .find(|p| p.is_file())
    }

    /// `ANDROID_HOME`, or the default the Android build script uses.
    pub fn android_home(&self) -> PathBuf {
        match self.var("ANDROID_HOME").filter(|v| !v.is_empty()) {
            Some(h) => PathBuf::from(h),
            None => self.home().join("Library/Android/sdk"),
        }
    }

    /// `HOME`.
    pub fn home(&self) -> PathBuf {
        self.var("HOME").map(PathBuf::from).unwrap_or_default()
    }

    /// The Android user directory, where the debug keystore lives:
    /// `ANDROID_USER_HOME`, else `ANDROID_SDK_HOME/.android`, else
    /// `~/.android` (the order the SDK tools use, and the build script's).
    pub fn android_user_dir(&self) -> PathBuf {
        if let Some(d) = self.var("ANDROID_USER_HOME").filter(|v| !v.is_empty()) {
            return PathBuf::from(d);
        }
        if let Some(d) = self.var("ANDROID_SDK_HOME").filter(|v| !v.is_empty()) {
            return PathBuf::from(d).join(".android");
        }
        self.home().join(".android")
    }
}

/// A digest over the inputs of a shell build. Every entry is labelled and
/// length-prefixed, so two different input sets never hash the same bytes.
pub struct Inputs {
    h: Sha256,
}

impl Inputs {
    /// A digest for one kind of shell build (`kind` keeps an iOS digest from
    /// ever matching an Android one).
    pub fn new(kind: &str) -> Self {
        let mut i = Inputs { h: Sha256::new() };
        i.text("kind", kind);
        i
    }

    /// Add `bytes` under `label`.
    pub fn bytes(&mut self, label: &str, bytes: &[u8]) {
        for part in [label.as_bytes(), bytes] {
            self.h.update(&(part.len() as u64).to_le_bytes());
            self.h.update(part);
        }
    }

    /// Add a string under `label`.
    pub fn text(&mut self, label: &str, s: &str) {
        self.bytes(label, s.as_bytes());
    }

    /// Add an OS string (a path, an environment value) under `label`.
    pub fn os(&mut self, label: &str, s: Option<&OsStr>) {
        match s {
            Some(s) => self.bytes(label, s.as_encoded_bytes()),
            None => self.bytes(label, b"\0absent"),
        }
    }

    /// Add the bytes of the file at `path`, or that it is absent.
    pub fn file(&mut self, label: &str, path: &Path) {
        self.os(&format!("{label} path"), Some(path.as_os_str()));
        match std::fs::read(path) {
            Ok(b) => self.bytes(label, &b),
            Err(_) => self.bytes(label, b"\0absent"),
        }
    }

    /// Add every file under `dir`: its path relative to `dir` and its bytes,
    /// in sorted order. An absent directory hashes as empty.
    pub fn tree(&mut self, label: &str, dir: &Path) {
        let mut files = Vec::new();
        walk_files(dir, &mut files);
        files.sort();
        self.text(&format!("{label} files"), &files.len().to_string());
        for f in files {
            let rel = f.strip_prefix(dir).unwrap_or(&f).to_path_buf();
            self.os(&format!("{label} entry"), Some(rel.as_os_str()));
            self.file(&format!("{label} bytes"), &f);
        }
    }

    /// Add a tool binary's identity: its resolved path, size and mtime (a
    /// toolchain update replaces the file). Runs nothing.
    pub fn stat(&mut self, label: &str, path: Option<&Path>) {
        let Some(path) = path else {
            self.os(label, None);
            return;
        };
        let real = path.canonicalize().unwrap_or_else(|_| path.to_path_buf());
        self.os(&format!("{label} path"), Some(real.as_os_str()));
        match std::fs::metadata(&real) {
            Ok(m) => {
                let mtime = m
                    .modified()
                    .ok()
                    .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
                    .map(|d| d.as_nanos())
                    .unwrap_or(0);
                self.text(&format!("{label} stat"), &format!("{} {mtime}", m.len()));
            }
            Err(_) => self.os(&format!("{label} stat"), None),
        }
    }

    /// Add the identity of the tool `name` found on the environment's `PATH`.
    pub fn tool(&mut self, env: &ToolEnv, name: &str) {
        self.stat(&format!("tool {name}"), env.which(name).as_deref());
    }

    /// Add the trimmed standard output of a command, or that it failed.
    pub fn output(&mut self, label: &str, cmd: &mut Command) -> Option<String> {
        let out = cmd
            .output()
            .ok()
            .filter(|o| o.status.success())
            .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string());
        match &out {
            Some(s) => self.text(label, s),
            None => self.os(label, None),
        }
        out
    }

    /// The hex digest.
    pub fn finish(self) -> String {
        self.h.finish().iter().map(|b| format!("{b:02x}")).collect()
    }
}

fn walk_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for e in rd.flatten() {
        let p = e.path();
        match e.file_type() {
            Ok(t) if t.is_dir() => walk_files(&p, out),
            Ok(_) => out.push(p),
            Err(_) => {}
        }
    }
}

/// The file in a shell's `build/` directory holding the digest of the inputs
/// it was built from. It is written last, after every tool step succeeded.
pub const STAMP: &str = ".sky-inputs";

/// Whether `build_dir` holds a complete build of exactly these inputs: its
/// stamp is `digest` and every one of `outputs` exists.
pub fn up_to_date(build_dir: &Path, digest: &str, outputs: &[PathBuf]) -> bool {
    std::fs::read_to_string(build_dir.join(STAMP)).is_ok_and(|s| s == digest)
        && outputs.iter().all(|o| o.exists())
}

/// Forget the stamp before a rebuild, so a build that fails part-way is never
/// taken for a complete one.
pub fn clear_stamp(build_dir: &Path) {
    let _ = std::fs::remove_file(build_dir.join(STAMP));
}

/// Record that `build_dir` now holds a complete build of `digest`.
pub fn mark_built(build_dir: &Path, digest: &str) -> Result<(), String> {
    std::fs::write(build_dir.join(STAMP), digest)
        .map_err(|e| format!("write {}: {e}", build_dir.join(STAMP).display()))
}

/// Write `bytes` to `path` unless it already holds exactly those bytes.
pub fn write_bytes_if_changed(path: &Path, bytes: &[u8]) -> Result<(), String> {
    if std::fs::read(path).is_ok_and(|b| b == bytes) {
        return Ok(());
    }
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| format!("create {}: {e}", parent.display()))?;
    }
    std::fs::write(path, bytes).map_err(|e| format!("write {}: {e}", path.display()))
}

/// Copy `src` to `dst` unless `dst` already holds the same bytes.
pub fn copy_if_changed(src: &Path, dst: &Path) -> Result<(), String> {
    let bytes = std::fs::read(src).map_err(|e| format!("read {}: {e}", src.display()))?;
    write_bytes_if_changed(dst, &bytes)
}

/// Remove every file under `dir` that is not in `keep`, then every directory
/// left empty. A shell's generated tree is kept between builds, so a file an
/// earlier build wrote and this one does not (a removed native extension, the
/// sources of an old bundle id) must not stay and be compiled in.
pub fn prune_tree(dir: &Path, keep: &BTreeSet<PathBuf>) {
    let mut files = Vec::new();
    walk_files(dir, &mut files);
    for f in files {
        if !keep.contains(&f) {
            let _ = std::fs::remove_file(&f);
        }
    }
    remove_empty_dirs(dir);
}

fn remove_empty_dirs(dir: &Path) -> bool {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return false;
    };
    let mut empty = true;
    for e in rd.flatten() {
        let p = e.path();
        if e.file_type().is_ok_and(|t| t.is_dir()) {
            if remove_empty_dirs(&p) {
                let _ = std::fs::remove_dir(&p);
            } else {
                empty = false;
            }
        } else {
            empty = false;
        }
    }
    empty
}

/// The identity of the iOS simulator toolchain at `developer_dir`: the Xcode
/// version, the simulator SDK and its settings, the swiftc the build resolves,
/// and the other tools the build runs. `xcrun --show-sdk-path` and
/// `xcrun --find` only look up paths; no compiler runs.
pub fn ios_tools(i: &mut Inputs, env: &ToolEnv, developer_dir: &OsStr) {
    let dev = Path::new(developer_dir);
    i.os("DEVELOPER_DIR", Some(developer_dir));
    i.os("TOOLCHAINS", env.var("TOOLCHAINS").as_deref());
    if let Some(contents) = dev.parent() {
        i.file("xcode version", &contents.join("version.plist"));
    }
    let xcrun = |args: &[&str]| {
        let mut c = env.command("xcrun");
        c.args(args)
            .env("DEVELOPER_DIR", developer_dir)
            .env_remove("SDKROOT");
        c
    };
    if let Some(sdk) = i.output(
        "sdk path",
        &mut xcrun(&["--sdk", "iphonesimulator", "--show-sdk-path"]),
    ) {
        let sdk = PathBuf::from(sdk);
        i.file("sdk settings", &sdk.join("SDKSettings.json"));
        i.file(
            "sdk version",
            &sdk.join("System/Library/CoreServices/SystemVersion.plist"),
        );
    }
    let swiftc = i.output(
        "swiftc path",
        &mut xcrun(&["--sdk", "iphonesimulator", "--find", "swiftc"]),
    );
    i.stat("swiftc", swiftc.as_deref().map(Path::new));
    for t in ["bash", "xcrun", "derq", "codesign", "sips"] {
        i.tool(env, t);
    }
}

/// The identity of the Android toolchain: every installed build-tools and
/// platform (the build script picks the newest of each), the JDK, the tools
/// the script runs, and the debug keystore that signs the APK.
pub fn android_tools(i: &mut Inputs, env: &ToolEnv) {
    let sdk = env.android_home();
    i.os("ANDROID_HOME", Some(sdk.as_os_str()));
    for (sub, files) in [
        (
            "build-tools",
            &["source.properties", "aapt2", "d8", "zipalign", "apksigner"][..],
        ),
        ("platforms", &["source.properties", "android.jar"][..]),
    ] {
        let mut dirs: Vec<PathBuf> = std::fs::read_dir(sdk.join(sub))
            .map(|rd| rd.flatten().map(|e| e.path()).collect())
            .unwrap_or_default();
        dirs.sort();
        i.text(&format!("{sub} count"), &dirs.len().to_string());
        for d in dirs {
            i.os(&format!("{sub} dir"), Some(d.as_os_str()));
            i.file(&format!("{sub} source.properties"), &d.join(files[0]));
            for f in &files[1..] {
                i.stat(&format!("{sub} {f}"), Some(&d.join(f)));
            }
        }
    }
    // The JDK: javac on PATH, and the JDK it runs (macOS's /usr/bin/javac
    // picks one through JAVA_HOME or java_home).
    i.tool(env, "javac");
    let java_home = env.var("JAVA_HOME").filter(|v| !v.is_empty());
    i.os("JAVA_HOME", java_home.as_deref());
    let jdk = match java_home {
        Some(h) => Some(PathBuf::from(h)),
        None if Path::new("/usr/libexec/java_home").is_file() => i
            .output("java_home", &mut env.command("/usr/libexec/java_home"))
            .map(PathBuf::from),
        None => None,
    };
    if let Some(jdk) = jdk {
        i.file("jdk release", &jdk.join("release"));
        i.stat("jdk javac", Some(&jdk.join("bin/javac")));
    }
    for t in ["bash", "keytool", "zip"] {
        i.tool(env, t);
    }
    i.file(
        "debug keystore",
        &env.android_user_dir().join("debug.keystore"),
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("sky-shellin-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    fn digest(dir: &Path) -> String {
        let mut i = Inputs::new("t");
        i.tree("src", dir);
        i.finish()
    }

    #[test]
    fn a_tree_digest_follows_bytes_not_mtimes() {
        let d = tmp("tree");
        std::fs::create_dir_all(d.join("a")).unwrap();
        std::fs::write(d.join("a/x.swift"), "x").unwrap();
        let first = digest(&d);
        // Rewritten with the same bytes: the same digest.
        std::fs::write(d.join("a/x.swift"), "x").unwrap();
        assert_eq!(digest(&d), first);
        std::fs::write(d.join("a/x.swift"), "y").unwrap();
        assert_ne!(digest(&d), first, "changed bytes change the digest");
        std::fs::write(d.join("a/x.swift"), "x").unwrap();
        std::fs::rename(d.join("a/x.swift"), d.join("a/z.swift")).unwrap();
        assert_ne!(digest(&d), first, "a renamed file changes the digest");
        std::fs::rename(d.join("a/z.swift"), d.join("a/x.swift")).unwrap();
        std::fs::write(d.join("b.swift"), "").unwrap();
        assert_ne!(digest(&d), first, "an added empty file changes the digest");
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn labels_keep_entries_apart() {
        let mut a = Inputs::new("t");
        a.text("x", "ab");
        a.text("y", "");
        let mut b = Inputs::new("t");
        b.text("x", "a");
        b.text("y", "b");
        assert_ne!(a.finish(), b.finish());
        assert_ne!(Inputs::new("ios").finish(), Inputs::new("android").finish());
    }

    #[test]
    fn a_stamp_needs_the_digest_and_every_output() {
        let d = tmp("stamp");
        let out = d.join("app.apk");
        assert!(!up_to_date(&d, "abc", std::slice::from_ref(&out)));
        mark_built(&d, "abc").unwrap();
        assert!(
            !up_to_date(&d, "abc", std::slice::from_ref(&out)),
            "no output"
        );
        std::fs::write(&out, "").unwrap();
        assert!(up_to_date(&d, "abc", std::slice::from_ref(&out)));
        assert!(
            !up_to_date(&d, "abd", std::slice::from_ref(&out)),
            "other inputs"
        );
        clear_stamp(&d);
        assert!(!up_to_date(&d, "abc", std::slice::from_ref(&out)));
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn prune_removes_what_this_build_did_not_write() {
        let d = tmp("prune");
        let keep = d.join("com/acme/app/MainActivity.java");
        let stale = d.join("com/old/app/MainActivity.java");
        for f in [&keep, &stale] {
            std::fs::create_dir_all(f.parent().unwrap()).unwrap();
            std::fs::write(f, "class X {}").unwrap();
        }
        prune_tree(&d, &BTreeSet::from([keep.clone()]));
        assert!(keep.is_file());
        assert!(!stale.exists());
        assert!(!d.join("com/old").exists(), "an emptied directory goes too");
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn a_write_of_the_same_bytes_keeps_the_mtime() {
        let d = tmp("write");
        let f = d.join("x/Info.plist");
        write_bytes_if_changed(&f, b"a").unwrap();
        let t = std::time::UNIX_EPOCH + std::time::Duration::from_secs(1_000_000);
        std::fs::File::options()
            .append(true)
            .open(&f)
            .unwrap()
            .set_modified(t)
            .unwrap();
        write_bytes_if_changed(&f, b"a").unwrap();
        assert_eq!(std::fs::metadata(&f).unwrap().modified().unwrap(), t);
        write_bytes_if_changed(&f, b"b").unwrap();
        assert_eq!(std::fs::read(&f).unwrap(), b"b");
        let _ = std::fs::remove_dir_all(&d);
    }
}
