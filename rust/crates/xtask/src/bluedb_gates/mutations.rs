//! `--verify-mutations` — the scratch-worktree mutation runner (§9.4).
//!
//! A gate does not count until it has been proven falsifiable **by mutation**:
//! reintroduce the defect, watch the gate go red, restore, record both outputs.
//!
//! # H3 — why the runner needs its own falsifier
//!
//! §9.4 step 3's emphasis is the whole of H3. If the runner applies the patch in
//! the scratch worktree but **builds or runs the gate against the developer's
//! tree** — an absolute `CARGO_TARGET_DIR`, an inherited `cwd`, a Go build that
//! resolves `runtime-go/` from the repo root — then the mutated code never
//! executes, every gate stays green under mutation, and every mutation reports
//! `PROVEN` forever. The verifier that certifies every other gate would itself
//! be unfalsifiable.
//!
//! Four independent mechanisms stop that here:
//!
//! 1. **The binary is rebuilt inside the worktree** with `CARGO_TARGET_DIR`
//!    pointed at the scratch root, and the runner asserts the binary it is
//!    about to execute lives under the scratch root.
//! 2. **The child prints the root it resolved** (`PROBE root=…`) and the runner
//!    asserts that root is inside the worktree. Because `repo_root()` derives
//!    from `env!("CARGO_MANIFEST_DIR")`, a binary built from the dev tree
//!    reports the dev tree — and is rejected.
//! 3. **The dev tree is checked for contamination** after every `git apply`:
//!    the patch's declared `targets` must be exactly as clean as they were
//!    before the run.
//! 4. **The canary `G0.C`** — asserts `true`, paired with a no-op patch. A
//!    correct verifier reports `VACUOUS`; `PROVEN` is a harness FAIL, because a
//!    gate that cannot go red cannot have been proven. The canary's patch also
//!    touches a sentinel path, so the runner can assert the *worktree* was
//!    modified and the *dev tree* was not.
//!
//! # A case §9.4 does not name
//!
//! §9.4's table assumes the gate is GREEN before the patch, and classifies on
//! the exit code alone. That is not sufficient. A gate can be red for an
//! unrelated reason — G0.4 is red on four pre-existing dead config keys, G0.7
//! on fifty-eight untagged citations — and under §9.4's rule every such gate
//! "goes red" under any patch and reports `PROVEN` without the patch having
//! done anything. That is the same green lie the canary exists to catch, one
//! level down.
//!
//! The runner therefore classifies on the **discriminating assertion**, not on
//! the exit code: the mutation's `expect` string must be **absent from the
//! baseline output and present after the patch**. A mutation whose assertion
//! already fires before the patch is `INCONCLUSIVE-BASELINE-RED` — it proves
//! nothing — and a patch that does not make the assertion fire is `VACUOUS`.
//! This proves falsifiability of the specific property even when the gate has
//! other, unrelated failures, which is exactly the situation P0 ships in.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::Command;

use super::gates_g0::expected_path;
use super::registry::{Mutation, CANARY_ID, REGISTRY};
use super::state::{GateState as Ledger, ProofOutcome};

/// Removes the scratch worktree on every exit path, including panic.
struct WorktreeGuard {
    repo: PathBuf,
    scratch: PathBuf,
    wt: PathBuf,
}

impl WorktreeGuard {
    fn create(repo: &Path) -> Result<WorktreeGuard, String> {
        // Outside the repo working tree, so it can never pollute `git status`.
        let scratch = std::env::temp_dir().join(format!(
            "sky-bluedb-mutverify-{}-{}",
            std::process::id(),
            now_millis()
        ));
        std::fs::create_dir_all(&scratch).map_err(|e| format!("scratch mkdir: {e}"))?;
        let wt = scratch.join("wt");

        let out = Command::new("git")
            .args(["worktree", "add", "--detach", "--quiet"])
            .arg(&wt)
            .arg("HEAD")
            .current_dir(repo)
            .output()
            .map_err(|e| format!("git worktree add: {e}"))?;
        if !out.status.success() {
            let _ = std::fs::remove_dir_all(&scratch);
            return Err(format!(
                "git worktree add failed: {}",
                String::from_utf8_lossy(&out.stderr).trim()
            ));
        }

        Ok(WorktreeGuard {
            repo: repo.to_path_buf(),
            scratch,
            wt,
        })
    }

    fn reset(&self) -> Result<(), String> {
        run_ok(Command::new("git").args(["reset", "--hard", "--quiet"]).current_dir(&self.wt))?;
        run_ok(Command::new("git").args(["clean", "-fdq"]).current_dir(&self.wt))
    }
}

impl Drop for WorktreeGuard {
    fn drop(&mut self) {
        let _ = Command::new("git")
            .args(["worktree", "remove", "--force"])
            .arg(&self.wt)
            .current_dir(&self.repo)
            .output();
        let _ = Command::new("git")
            .args(["worktree", "prune"])
            .current_dir(&self.repo)
            .output();
        let _ = std::fs::remove_dir_all(&self.scratch);
    }
}

fn now_millis() -> u128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0)
}

fn run_ok(cmd: &mut Command) -> Result<(), String> {
    let out = cmd.output().map_err(|e| e.to_string())?;
    if out.status.success() {
        Ok(())
    } else {
        Err(String::from_utf8_lossy(&out.stderr).trim().to_string())
    }
}

pub fn head_sha(root: &Path) -> String {
    Command::new("git")
        .args(["rev-parse", "--short=8", "HEAD"])
        .current_dir(root)
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_else(|| "unknown".into())
}

/// MAJOR-17: has any of the mutation's declared `targets` changed between the
/// sha the proof was taken at and `HEAD`?
///
/// A whole-tree "has anything changed" probe would mark everything unverified
/// after every commit, and a signal that always fires is a signal nobody reads —
/// which is why `Mutation` carries `targets` at all. An unresolvable sha is
/// treated as moved: unknown provenance is not evidence of freshness.
pub fn targets_moved(root: &Path, sha: &str, targets: &[&str]) -> bool {
    let mut cmd = Command::new("git");
    cmd.args(["diff", "--name-only", sha, "HEAD", "--"])
        .args(targets)
        .current_dir(root);
    match cmd.output() {
        Ok(o) if o.status.success() => !String::from_utf8_lossy(&o.stdout).trim().is_empty(),
        _ => true,
    }
}

/// `git status --porcelain` restricted to the given paths.
fn status_of(root: &Path, paths: &[&str]) -> String {
    Command::new("git")
        .args(["status", "--porcelain", "--"])
        .args(paths)
        .current_dir(root)
        .output()
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
        .unwrap_or_default()
}

struct ProbeResult {
    state: String,
    root: String,
    output: String,
    exit_ok: bool,
}

/// Build `xtask` **inside the worktree** and run one gate there.
fn probe(guard: &WorktreeGuard, gate_id: &str, verbose: bool) -> Result<ProbeResult, String> {
    let target_dir = guard.scratch.join("target");
    let build = Command::new("cargo")
        .args(["build", "--quiet", "-p", "xtask"])
        .current_dir(guard.wt.join("rust"))
        .env("CARGO_TARGET_DIR", &target_dir)
        .output()
        .map_err(|e| format!("cargo build in worktree: {e}"))?;
    if !build.status.success() {
        return Err(format!(
            "cargo build failed in the scratch worktree:\n{}",
            tail(&String::from_utf8_lossy(&build.stderr), 40)
        ));
    }

    let bin = target_dir.join("debug").join("xtask");
    // H3 mechanism 1 — the binary we are about to run must live under the
    // scratch root, never in the developer's target dir.
    if !bin.starts_with(&guard.scratch) || !bin.exists() {
        return Err(format!(
            "refusing to run {}: the mutation probe binary must live under the scratch root {}",
            bin.display(),
            guard.scratch.display()
        ));
    }

    let out = Command::new(&bin)
        .args(["bluedb-gates", "--mutation-probe", &format!("--only={gate_id}")])
        .current_dir(&guard.wt)
        .output()
        .map_err(|e| format!("probe run: {e}"))?;

    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    if verbose {
        println!("{}", tail(&text, 40));
    }

    let mut state = String::new();
    let mut root = String::new();
    for line in text.lines() {
        if let Some(rest) = line.strip_prefix("PROBE ") {
            for kv in rest.split_whitespace() {
                if let Some(v) = kv.strip_prefix("state=") {
                    state = v.to_string();
                }
                if let Some(v) = kv.strip_prefix("root=") {
                    root = v.to_string();
                }
            }
        }
    }
    if state.is_empty() {
        return Err(format!(
            "probe emitted no `PROBE state=` line:\n{}",
            tail(&text, 40)
        ));
    }

    Ok(ProbeResult {
        state,
        root,
        output: text,
        exit_ok: out.status.success(),
    })
}

fn tail(s: &str, n: usize) -> String {
    let lines: Vec<&str> = s.lines().collect();
    lines[lines.len().saturating_sub(n)..].join("\n")
}

pub struct VerifyReport {
    pub outcomes: BTreeMap<String, ProofOutcome>,
    pub notes: Vec<String>,
    pub canary_ok: bool,
    pub failures: Vec<String>,
}

/// Run every registered mutation. Returns the report; the caller writes the
/// ledger and decides the exit code.
pub fn verify_all(root: &Path, verbose: bool, only: Option<&str>) -> Result<VerifyReport, String> {
    let guard = WorktreeGuard::create(root)?;
    println!(
        "verify-mutations: scratch worktree {} (target dir {}/target)",
        guard.wt.display(),
        guard.scratch.display()
    );

    let sha = head_sha(root);
    let mut report = VerifyReport {
        outcomes: BTreeMap::new(),
        notes: Vec::new(),
        canary_ok: false,
        failures: Vec::new(),
    };

    for gate in REGISTRY {
        if let Some(o) = only {
            if gate.id != o {
                continue;
            }
        }
        for m in gate.mutations.as_slice() {
            let outcome = verify_one(root, &guard, gate.id, m, verbose, &mut report);
            let label = outcome.map(|o| o.label().to_string());
            match (outcome, label) {
                (Some(o), Some(l)) => {
                    println!("  {:<34} {}", m.id, l);
                    report.outcomes.insert(m.id.to_string(), o);

                    let want = if gate.id == CANARY_ID {
                        ProofOutcome::Vacuous
                    } else {
                        ProofOutcome::Proven
                    };
                    if gate.id == CANARY_ID {
                        report.canary_ok = o == ProofOutcome::Vacuous;
                        if o == ProofOutcome::Proven {
                            report.failures.push(format!(
                                "HARNESS FAIL: the canary {} reported PROVEN. A gate that asserts `true` cannot go red, so the runner is not measuring what it claims (H3).",
                                m.id
                            ));
                        } else if o != ProofOutcome::Vacuous {
                            report.failures.push(format!(
                                "HARNESS FAIL: the canary {} reported {} — expected VACUOUS.",
                                m.id,
                                o.label()
                            ));
                        }
                    } else if o != want {
                        report.failures.push(format!(
                            "{}: {} (required: {})",
                            m.id,
                            o.label(),
                            want.label()
                        ));
                    }
                }
                _ => {
                    println!("  {:<34} PENDING (gate not implemented yet)", m.id);
                }
            }
        }
    }

    // Record the sha each verdict was taken at.
    let mut ledger = Ledger::load(root);
    for (id, o) in &report.outcomes {
        ledger.proofs.insert(id.clone(), (*o, sha.clone()));
    }
    ledger
        .save(root)
        .map_err(|e| format!("writing the proof ledger: {e}"))?;

    Ok(report)
}

/// `None` means the gate has no implementable body yet (its baseline is
/// `NOT RUN`), so the mutation is not attempted. That is never a pass: a
/// `NOT RUN` gate already renders its goal `UNKNOWN`.
fn verify_one(
    root: &Path,
    guard: &WorktreeGuard,
    gate_id: &str,
    m: &Mutation,
    verbose: bool,
    report: &mut VerifyReport,
) -> Option<ProofOutcome> {
    if let Err(e) = guard.reset() {
        report.failures.push(format!("{}: worktree reset failed: {e}", m.id));
        return Some(ProofOutcome::MutationStale);
    }

    // --- baseline, measured in the worktree ------------------------------
    let base = match probe(guard, gate_id, verbose) {
        Ok(p) => p,
        Err(e) => {
            report.failures.push(format!("{}: baseline probe failed: {e}", m.id));
            return Some(ProofOutcome::MutationStale);
        }
    };
    if let Some(bad) = wrong_tree(guard, &base.root) {
        report.failures.push(format!("{}: {bad}", m.id));
        return Some(ProofOutcome::WrongTree);
    }
    if base.state == "NOT_RUN" {
        return None;
    }
    // The discriminating assertion must not already be firing.
    if m.expect != "<never>" && base.output.contains(m.expect) {
        report.notes.push(format!(
            "{}: the assertion {:?} already fires before the patch — the mutation proves nothing about it",
            m.id, m.expect
        ));
        return Some(ProofOutcome::InconclusiveBaselineRed);
    }
    if m.expect == "<never>" && !base.exit_ok {
        report.notes.push(format!(
            "{}: gate {gate_id} was already RED before the patch and declares no discriminating assertion",
            m.id
        ));
        return Some(ProofOutcome::InconclusiveBaselineRed);
    }

    // --- apply, in the worktree only -------------------------------------
    let patch = root.join(m.patch);
    if !patch.exists() {
        report
            .notes
            .push(format!("{}: no patch file at {}", m.id, m.patch));
        return Some(ProofOutcome::MutationStale);
    }
    let dev_before = status_of(root, m.targets);
    let applied = Command::new("git")
        .args(["apply", "--whitespace=nowarn"])
        .arg(&patch)
        .current_dir(&guard.wt)
        .output()
        .map_err(|e| e.to_string());
    match applied {
        Ok(o) if o.status.success() => {}
        Ok(o) => {
            report.notes.push(format!(
                "{}: patch no longer applies: {}",
                m.id,
                String::from_utf8_lossy(&o.stderr).trim()
            ));
            return Some(ProofOutcome::MutationStale);
        }
        Err(e) => {
            report.notes.push(format!("{}: git apply: {e}", m.id));
            return Some(ProofOutcome::MutationStale);
        }
    }

    // H3 mechanism 3 — the patch must have landed in the worktree and NOT in
    // the developer's tree.
    if status_of(&guard.wt, m.targets).trim().is_empty() {
        report.failures.push(format!(
            "{}: `git apply` reported success but the worktree is unchanged at {:?}",
            m.id, m.targets
        ));
        return Some(ProofOutcome::WrongTree);
    }
    let dev_after = status_of(root, m.targets);
    if dev_after != dev_before {
        report.failures.push(format!(
            "HARNESS FAIL: {} contaminated the developer's tree at {:?} — the mutation runner must never modify the tree it is certifying (H3)",
            m.id, m.targets
        ));
        return Some(ProofOutcome::WrongTree);
    }

    // --- the canary's sentinel arm ---------------------------------------
    if gate_id == CANARY_ID {
        let sentinel = "docs/bluedb/mutations/CANARY_TOUCHED";
        if !guard.wt.join(sentinel).exists() {
            report.failures.push(format!(
                "HARNESS FAIL: the canary patch applied but {sentinel} is absent from the worktree — the runner is not writing where it thinks it is (H3)"
            ));
            return Some(ProofOutcome::WrongTree);
        }
        if root.join(sentinel).exists() {
            report.failures.push(format!(
                "HARNESS FAIL: {sentinel} appeared in the DEVELOPER's tree — the runner applied the canary patch to the wrong tree (H3)"
            ));
            return Some(ProofOutcome::WrongTree);
        }
    }

    // --- run the mutated gate, built from and executed in the worktree ----
    let red = match probe(guard, gate_id, verbose) {
        Ok(p) => p,
        Err(e) => {
            report
                .failures
                .push(format!("{}: mutated probe failed: {e}", m.id));
            return Some(ProofOutcome::MutationStale);
        }
    };
    if let Some(bad) = wrong_tree(guard, &red.root) {
        report.failures.push(format!("{}: {bad}", m.id));
        return Some(ProofOutcome::WrongTree);
    }

    if m.expect == "<never>" {
        // The canary: it asserts `true`, so staying green IS the correct
        // answer, and going red would mean the runner is not measuring what it
        // claims.
        return Some(if red.exit_ok {
            ProofOutcome::Vacuous
        } else {
            ProofOutcome::Proven
        });
    }

    if red.exit_ok || !red.output.contains(m.expect) {
        report.notes.push(format!(
            "{}: the patch applied but the assertion {:?} did not fire — the gate does not detect the defect it claims to",
            m.id, m.expect
        ));
        return Some(ProofOutcome::Vacuous);
    }

    // Record the RED output verbatim (§9.4 — "the proof is a patch plus two
    // recorded outputs"). Written to the dev tree, where it is committed.
    let expected = root.join(expected_path(m.patch));
    if let Some(parent) = expected.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let _ = std::fs::write(&expected, &red.output);

    Some(ProofOutcome::Proven)
}

/// H3 mechanism 2 — the child must have resolved a root inside the worktree.
fn wrong_tree(guard: &WorktreeGuard, reported_root: &str) -> Option<String> {
    if reported_root.is_empty() {
        return Some("the probe did not report the root it resolved".to_string());
    }
    let reported = std::fs::canonicalize(reported_root).unwrap_or_else(|_| PathBuf::from(reported_root));
    let wt = std::fs::canonicalize(&guard.wt).unwrap_or_else(|_| guard.wt.clone());
    if reported.starts_with(&wt) {
        None
    } else {
        Some(format!(
            "HARNESS FAIL: the probe resolved root {} — outside the scratch worktree {}. The mutated code never executed, so every mutation would report PROVEN forever (H3).",
            reported.display(),
            wt.display()
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tail_keeps_the_last_lines() {
        assert_eq!(tail("a\nb\nc", 2), "b\nc");
        assert_eq!(tail("a", 5), "a");
    }

    #[test]
    fn unresolvable_sha_counts_as_moved() {
        // Conservative by construction: unknown provenance is not freshness.
        assert!(targets_moved(
            Path::new("."),
            "0000000000000000000000000000000000000000",
            &["docs"]
        ));
    }

    #[test]
    fn wrong_tree_rejects_a_root_outside_the_worktree() {
        let guard = WorktreeGuard {
            repo: PathBuf::from("/repo"),
            scratch: PathBuf::from("/scratch"),
            wt: PathBuf::from("/scratch/wt"),
        };
        assert!(wrong_tree(&guard, "/repo").is_some());
        assert!(wrong_tree(&guard, "").is_some());
        assert!(wrong_tree(&guard, "/scratch/wt").is_none());
        // Drop would try to remove a non-existent worktree; harmless.
        std::mem::forget(guard);
    }
}
