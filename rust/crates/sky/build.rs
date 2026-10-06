// `sky --version` prints the workspace version (`[workspace.package] version`
// in rust/Cargo.toml, read with `env!("CARGO_PKG_VERSION")`), followed by the
// commit when the build knows one: `sky v0.27.5 (1a2b3c4)`. This script bakes
// that commit as `SKY_BUILD_GIT_REV`:
//
//   * `SKY_GIT_REV` set in the environment wins, even when empty. Nix sets it
//     (the flake to the short revision, the stable `nix-build` to ""), and the
//     release workflow sets it to "" so a released binary prints exactly
//     `sky v<tag>`.
//   * Else, inside a git checkout, `git rev-parse --short HEAD`, with `-dirty`
//     when tracked files differ from HEAD.
//   * Else nothing.
//
// A build that carries a commit is a source build: `sky upgrade` asks for
// `--force` before it replaces one, and the update nudge stays quiet.
//
// The migration guide's SILENT bullets are embedded here, from the one source
// `docs/migration/v0.27.md`, for the notice the first run of a new `sky`
// version prints (`src/version_notice.rs`). A bullet is embedded when its line
// starts with `- ` and carries the word `SILENT`, or when it sits under a
// heading whose text contains `Silent`. A release build (a tag build in the
// release workflow, `GITHUB_REF_TYPE=tag`) refuses to build without the guide;
// any other build embeds a loud placeholder instead of an empty text.
use std::path::{Path, PathBuf};
use std::process::Command;

const GUIDE: &str = "docs/migration/v0.27.md";

fn main() {
    println!("cargo:rerun-if-env-changed=SKY_GIT_REV");
    println!("cargo:rerun-if-env-changed=GITHUB_REF_TYPE");
    let manifest = PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").unwrap());
    let rev = match std::env::var("SKY_GIT_REV") {
        Ok(v) => v.trim().to_string(),
        Err(_) => git_rev(&manifest),
    };
    println!("cargo:rustc-env=SKY_BUILD_GIT_REV={rev}");
    let guide = manifest.join("../../..").join(GUIDE);
    println!("cargo:rerun-if-changed={}", guide.display());
    let out = PathBuf::from(std::env::var("OUT_DIR").unwrap()).join("migration_silent.txt");
    let text = match std::fs::read_to_string(&guide) {
        Ok(md) => {
            let bullets = silent_bullets(&md);
            if bullets.is_empty() && is_release_build() {
                panic!(
                    "{GUIDE} has no SILENT bullets: the first-run upgrade notice would say \
                     nothing. Mark each silent change `SILENT` (or put them under a `Silent` \
                     heading)."
                );
            }
            bullets.join("\n")
        }
        Err(e) => {
            if is_release_build() {
                panic!(
                    "{GUIDE} is missing ({e}): a release must embed the migration guide \
                     the first-run upgrade notice prints."
                );
            }
            println!(
                "cargo:warning={GUIDE} is missing; the upgrade notice embeds a placeholder. \
                 A release build refuses this."
            );
            "- (this development build has no migration guide embedded: \
             docs/migration/v0.27.md was missing when it was built)"
                .to_string()
        }
    };
    std::fs::write(&out, text).unwrap();
}

/// The short revision of the checkout `dir` is in, with `-dirty` when tracked
/// files differ from HEAD; "" outside a git checkout or without `git`. It
/// also asks cargo to re-run this script when HEAD, the branch it names, or
/// the index moves.
fn git_rev(dir: &Path) -> String {
    // `--no-optional-locks`: `git status` must not rewrite the index it is
    // watched through, or every build would re-run this script.
    let git = |args: &[&str]| -> Option<String> {
        let out = Command::new("git")
            .arg("--no-optional-locks")
            .arg("-C")
            .arg(dir)
            .args(args)
            .output()
            .ok()?;
        out.status
            .success()
            .then(|| String::from_utf8_lossy(&out.stdout).trim().to_string())
    };
    let Some(rev) = git(&["rev-parse", "--short", "HEAD"]).filter(|r| !r.is_empty()) else {
        return String::new();
    };
    let mut watched = vec![
        "HEAD".to_string(),
        "index".to_string(),
        "packed-refs".to_string(),
    ];
    if let Some(r) = git(&["symbolic-ref", "-q", "HEAD"]) {
        watched.push(r);
    }
    for w in watched {
        // A watched path that does not exist would re-run this script on
        // every build, so only existing ones are named.
        if let Some(p) = git(&["rev-parse", "--path-format=absolute", "--git-path", &w])
            .filter(|p| Path::new(p).exists())
        {
            println!("cargo:rerun-if-changed={p}");
        }
    }
    let dirty =
        git(&["status", "--porcelain", "--untracked-files=no"]).is_some_and(|s| !s.is_empty());
    if dirty {
        format!("{rev}-dirty")
    } else {
        rev
    }
}

fn is_release_build() -> bool {
    std::env::var("GITHUB_REF_TYPE").is_ok_and(|v| v == "tag")
}

/// The SILENT bullets of the guide, each as one `- …` line with the marker
/// word removed.
fn silent_bullets(md: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut in_silent = false;
    for line in md.lines() {
        let t = line.trim();
        if t.starts_with('#') {
            in_silent = t.contains("Silent") || t.contains("SILENT");
            continue;
        }
        if let Some(rest) = t.strip_prefix("- ") {
            if in_silent || rest.contains("SILENT") {
                let clean = rest
                    .replace("**SILENT**", "")
                    .replace("(SILENT)", "")
                    .replace("SILENT:", "")
                    .replace("SILENT", "");
                let clean = clean.trim();
                if !clean.is_empty() {
                    out.push(format!("- {clean}"));
                }
            }
        }
    }
    out
}
