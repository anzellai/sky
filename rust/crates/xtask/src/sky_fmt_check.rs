//! `xtask sky-fmt-check` — every tracked `.sky` / `.skyi` file is `sky fmt`
//! clean (v0.27.16).
//!
//! The engineering norm is "run the formatter on every file you touch", and
//! nothing enforced it for Sky source outside the shipped trees. A scan of the
//! tree found 275 tracked files that `sky fmt --check` would reformat: test
//! fixtures under `rust/crates`, the probe fixtures, `test-files/` and the
//! repro corpus. A later blanket `sky fmt` then produces a diff that buries real
//! changes, which is the drift the norm exists to stop.
//!
//! This gate runs the same check `sky fmt --check` runs, in process
//! (`project::parse_errors` then `fmt::is_formatted`), over every file `git
//! ls-files` lists with a `.sky` or `.skyi` extension. A file that does not
//! parse fails too, as it does under `sky fmt --check`.
//!
//! The only files it skips are listed in `rust/crates/xtask/sky-fmt-exclusions.txt`,
//! one path or directory prefix per line, each followed by ` # <reason>`. An
//! entry with no reason, or one that matches no tracked file, fails the gate,
//! so the list cannot rot into a blanket waiver.
//!
//! Three assertions, so the harness can demand an exact count:
//!   1. at least [`MIN_FILES`] files were checked (an empty `git ls-files` or a
//!      wrong root cannot pass vacuously);
//!   2. every checked file parses and is formatted;
//!   3. every exclusion entry carries a reason and matches a tracked file.

use std::path::Path;
use std::process::Command;

/// The exclusion list, relative to the repo root.
pub const EXCLUSIONS: &str = "rust/crates/xtask/sky-fmt-exclusions.txt";

/// A floor on the number of files checked. The tree holds about 700 live Sky
/// files; a run that sees fewer than this is not looking at the tree.
pub const MIN_FILES: usize = 500;

/// The assertion count the harness pins.
pub const ASSERTIONS: u64 = 3;

pub struct Report {
    pub checked: usize,
    pub excluded: usize,
    pub unformatted: Vec<String>,
    pub unparsable: Vec<String>,
    pub bad_exclusions: Vec<String>,
    pub floor_ok: bool,
}

impl Report {
    pub fn ok(&self) -> bool {
        self.floor_ok
            && self.unformatted.is_empty()
            && self.unparsable.is_empty()
            && self.bad_exclusions.is_empty()
    }

    /// How many of the three assertions hold.
    pub fn passed(&self) -> u64 {
        u64::from(self.floor_ok)
            + u64::from(self.unformatted.is_empty() && self.unparsable.is_empty())
            + u64::from(self.bad_exclusions.is_empty())
    }
}

struct Exclusion {
    pattern: String,
    line: usize,
    has_reason: bool,
}

fn read_exclusions(root: &Path) -> Result<Vec<Exclusion>, String> {
    let text = std::fs::read_to_string(root.join(EXCLUSIONS))
        .map_err(|e| format!("could not read {EXCLUSIONS}: {e}"))?;
    let mut out = Vec::new();
    for (i, raw) in text.lines().enumerate() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let (pattern, reason) = match line.split_once(" # ") {
            Some((p, r)) => (p.trim(), r.trim()),
            None => (line, ""),
        };
        out.push(Exclusion {
            pattern: pattern.to_string(),
            line: i + 1,
            has_reason: !reason.is_empty(),
        });
    }
    Ok(out)
}

fn matches(pattern: &str, path: &str) -> bool {
    if pattern.ends_with('/') {
        path.starts_with(pattern)
    } else {
        path == pattern
    }
}

fn tracked_sky_files(root: &Path) -> Result<Vec<String>, String> {
    let out = Command::new("git")
        .args(["ls-files", "-z", "--", "*.sky", "*.skyi"])
        .current_dir(root)
        .output()
        .map_err(|e| format!("could not run git ls-files: {e}"))?;
    if !out.status.success() {
        return Err(format!(
            "git ls-files failed: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    let mut files: Vec<String> = out
        .stdout
        .split(|b| *b == 0)
        .filter(|s| !s.is_empty())
        .map(|s| String::from_utf8_lossy(s).into_owned())
        // USER RULE (anzellai/sky#299): examples/76-ai-shell-command is excluded
        // from CI in general; skip every `crate::ci_excluded` example by name.
        .filter(|f| !crate::ci_excluded::is_ci_excluded_rel(f))
        .collect();
    files.sort();
    Ok(files)
}

pub fn scan(root: &Path) -> Result<Report, String> {
    let exclusions = read_exclusions(root)?;
    let files = tracked_sky_files(root)?;
    let mut used = vec![false; exclusions.len()];
    let mut report = Report {
        checked: 0,
        excluded: 0,
        unformatted: Vec::new(),
        unparsable: Vec::new(),
        bad_exclusions: Vec::new(),
        floor_ok: false,
    };
    for f in &files {
        if let Some(i) = exclusions.iter().position(|x| matches(&x.pattern, f)) {
            used[i] = true;
            report.excluded += 1;
            continue;
        }
        // A tracked file deleted in the working tree is not ours to judge.
        let Ok(bytes) = std::fs::read(root.join(f)) else {
            continue;
        };
        report.checked += 1;
        let Ok(src) = String::from_utf8(bytes) else {
            report.unparsable.push(format!("{f} (not UTF-8)"));
            continue;
        };
        if !project::parse_errors(&src, f).is_empty() {
            report.unparsable.push(f.clone());
        } else if !fmt::is_formatted(&src) {
            report.unformatted.push(f.clone());
        }
    }
    for (x, used) in exclusions.iter().zip(&used) {
        if !x.has_reason {
            report.bad_exclusions.push(format!(
                "{EXCLUSIONS}:{}: `{}` has no ` # <reason>`",
                x.line, x.pattern
            ));
        } else if !used {
            report.bad_exclusions.push(format!(
                "{EXCLUSIONS}:{}: `{}` matches no tracked file (delete the entry)",
                x.line, x.pattern
            ));
        }
    }
    report.floor_ok = report.checked >= MIN_FILES;
    Ok(report)
}

pub fn run(_args: &[String], root: &Path) -> i32 {
    let report = match scan(root) {
        Ok(r) => r,
        Err(e) => {
            eprintln!("SKY-FMT-CHECK: {e}");
            return 1;
        }
    };
    for f in &report.unformatted {
        println!("would reformat: {f}");
    }
    for f in &report.unparsable {
        println!("does not parse: {f}");
    }
    for b in &report.bad_exclusions {
        println!("bad exclusion: {b}");
    }
    if !report.floor_ok {
        println!(
            "checked only {} file(s), fewer than the floor of {MIN_FILES}: is the root right?",
            report.checked
        );
    }
    if report.ok() {
        println!(
            "SKY-FMT-CHECK: PASS ({} file(s) formatted, {} excluded by {EXCLUSIONS})",
            report.checked, report.excluded
        );
        0
    } else {
        println!(
            "SKY-FMT-CHECK: FAIL ({} unformatted, {} unparsable, {} bad exclusion(s), {} checked). \
             Fix: run `sky fmt <file>` on each file above.",
            report.unformatted.len(),
            report.unparsable.len(),
            report.bad_exclusions.len(),
            report.checked
        );
        1
    }
}
