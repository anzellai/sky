// Release builds bake the tag into the binary via `SKY_BUILD_VERSION` (set by
// the release workflow). `option_env!` reads it at compile time; this line makes
// cargo recompile when the value changes so the baked version stays in sync.
//
// The migration guide's SILENT bullets are embedded here, from the one source
// `docs/migration/v0.27.md`, for the notice the first run of a new `sky`
// version prints (`src/version_notice.rs`). A bullet is embedded when its line
// starts with `- ` and carries the word `SILENT`, or when it sits under a
// heading whose text contains `Silent`. A release build (a tag build in the
// release workflow, `GITHUB_REF_TYPE=tag`) refuses to build without the guide;
// any other build embeds a loud placeholder instead of an empty text.
use std::path::PathBuf;

const GUIDE: &str = "docs/migration/v0.27.md";

fn main() {
    println!("cargo:rerun-if-env-changed=SKY_BUILD_VERSION");
    println!("cargo:rerun-if-env-changed=GITHUB_REF_TYPE");
    let manifest = PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").unwrap());
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
