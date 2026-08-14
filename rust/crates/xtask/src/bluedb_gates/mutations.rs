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
//! 4. **HEAD skew is refused up front** ([`head_skew`]). The worktree is HEAD,
//!    so an uncommitted change to anything the probe compiles or reads is
//!    invisible to it — while the parent, which classifies its output, is built
//!    from exactly those files. The runner will not start against a tree that
//!    disagrees with HEAD.
//! 5. **The canary `G0.C`** — asserts `true`, paired with a no-op patch. A
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

/// The trees the probe compiles or reads. It takes ALL of them from the scratch
/// worktree, and `git worktree add --detach HEAD` pins that to the last COMMIT.
const MEASURED_FROM_HEAD: &[&str] = &[
    "rust",
    "runtime-go",
    "sky-stdlib",
    "examples",
    "docs/bluedb",
];

/// The paths under [`MEASURED_FROM_HEAD`] that legitimately differ from HEAD
/// during a run, because the runner reads or writes them in the DEVELOPER's
/// tree by design:
///
/// * `gate-state.tsv` and `*.expected.txt` are the runner's own outputs — it
///   writes them mid-run, so including them would make every second run refuse.
/// * `mutations/*.patch` is read from the developer's tree and applied INTO the
///   worktree, so an uncommitted patch is the one actually measured. There is
///   no skew to warn about; that is the whole point of `--verify-mutations`
///   while authoring a falsification.
fn read_from_the_dev_tree(p: &str) -> bool {
    p == "docs/bluedb/gate-state.tsv"
        || p.ends_with(".expected.txt")
        || (p.starts_with("docs/bluedb/mutations/") && p.ends_with(".patch"))
}

/// `XY <path>`, or `XY <orig> -> <new>` for a rename. The NEW path is the one
/// that exists to be measured.
fn porcelain_path(line: &str) -> Option<String> {
    let rest = line.get(3..)?.trim();
    if rest.is_empty() {
        return None;
    }
    Some(match rest.split_once(" -> ") {
        Some((_, new)) => new.trim_matches('"').to_string(),
        None => rest.trim_matches('"').to_string(),
    })
}

/// **HEAD skew** — the working-tree changes the probe cannot see.
///
/// Everything the probe compiles or reads comes from the scratch worktree, and
/// that worktree is HEAD. A change that exists only in the developer's working
/// tree is therefore INVISIBLE to the child — while the PARENT, which applies
/// the patch, classifies the output and decides PROVEN or VACUOUS, is the
/// binary the developer just built FROM that working tree. The two run
/// different code, silently, and the verdict describes a program nobody wrote.
///
/// This is not hypothetical. G0.3's falsification reported `VACUOUS` for a full
/// session against a fix that was already written: `sky_compiler`'s
/// `SKY_BLUEDB_COMPILER` support was uncommitted, so the parent lent the probe a
/// compiler and the child — built from a HEAD that had never heard of the
/// variable — found none in the pristine worktree and went red with "neither
/// rust/target/release/sky nor sky-out/sky exists". Red for the wrong reason is
/// exactly what the discriminating classifier is built to refuse, so it refused,
/// correctly, and every attempt to debug it read the parent's source and found
/// nothing wrong with it. Committing the fix — nothing else — turned it
/// `PROVEN`.
///
/// The failure was silent in the direction that wastes a session, and the same
/// skew in the other direction (an uncommitted gate body that cannot fail) would
/// mint a `PROVEN` for code that is not in the repository. So the runner
/// refuses to start rather than measure a tree that is not the one under the
/// developer's cursor. An unrunnable `git status` counts as skew: unknown
/// provenance is not evidence of freshness (cf. [`targets_moved`]).
fn head_skew(root: &Path) -> Vec<String> {
    let out = Command::new("git")
        .args(["status", "--porcelain", "--"])
        .args(MEASURED_FROM_HEAD)
        .current_dir(root)
        .output();
    let o = match out {
        Ok(o) if o.status.success() => o,
        Ok(o) => {
            return vec![format!(
                "!! `git status` failed: {}",
                String::from_utf8_lossy(&o.stderr).trim()
            )]
        }
        Err(e) => return vec![format!("!! `git status` could not run: {e}")],
    };
    String::from_utf8_lossy(&o.stdout)
        .lines()
        .filter(|l| !l.trim().is_empty())
        .filter(|l| porcelain_path(l).is_none_or(|p| !read_from_the_dev_tree(&p)))
        .map(|l| l.trim_end().to_string())
        .collect()
}

/// Does this mutation change the COMPILER, rather than the tree it compiles?
///
/// This decides whether the probe may borrow a prebuilt `sky` from the
/// developer's tree, and it is the whole safety argument for doing so.
///
/// A gate like G0.3 must build its subject, and the scratch worktree is a fresh
/// `git worktree add` with no build artefacts — so there is no compiler at all.
/// That is why G0.3's falsification was VACUOUS: the gate went red with "no
/// compiler exists" rather than its declared assertion, and the discriminating
/// classifier correctly refused to call that PROVEN.
///
/// Borrowing is safe **only** when the mutation lives outside the compiler's
/// own source. The compiler resolves `sky-stdlib/` and `runtime-go/` by walking
/// up from the project directory, so with the subject inside the worktree those
/// assets — and any mutation to them — come from the worktree. The tool is
/// external; the thing under test never is.
///
/// When the mutation patches compiler source, a prebuilt binary would NOT
/// contain it: the probe would measure an unmutated compiler while reporting a
/// mutated tree. That is a silently weakened proof, which is worse than the
/// vacuity it replaces. Then we lend nothing and the gate falls back to in-root
/// paths.
///
/// The question is answered from the PATCH, never from the declared `targets`.
/// The two look interchangeable and are not: `targets` drives the
/// `UNVERIFIED-SINCE` decay check and is deliberately broader than the diff —
/// G0.3's name `rust/crates/project/src/build.rs`, because a change there could
/// invalidate the proof, even though the patch does not touch it. Reading
/// `targets` here would conflate "could this proof be stale?" with "does this
/// patch modify the compiler?" and leave G0.3 permanently vacuous.
///
/// The patch is exact rather than heuristic: `git apply` changes precisely what
/// the diff headers name, so there is no undeclared path to miss.
fn mutation_touches_compiler(root: &Path, m: &Mutation) -> bool {
    let Ok(patch) = std::fs::read_to_string(root.join(m.patch)) else {
        // Unreadable patch: assume the worst. A missing patch is already
        // MUTATION-STALE elsewhere; it must not also buy an external compiler.
        return true;
    };
    patch.lines().any(|l| {
        l.starts_with("diff --git ")
            && l.split_whitespace().skip(2).any(|p| {
                p.trim_start_matches("a/")
                    .trim_start_matches("b/")
                    .starts_with("rust/")
            })
    })
}

/// A prebuilt `sky` from the DEVELOPER's tree, for gates that must compile
/// something. Only ever consulted via [`mutation_touches_compiler`]. Returns
/// `None` rather than a bad path, so an absent compiler stays an honest gate
/// failure instead of becoming a confusing one.
fn dev_tree_compiler(root: &Path) -> Option<PathBuf> {
    ["rust/target/release/sky", "sky-out/sky"]
        .iter()
        .map(|c| root.join(c))
        .find(|p| p.is_file())
}

/// Build `xtask` **inside the worktree** and run one gate there.
fn probe(
    guard: &WorktreeGuard,
    gate_id: &str,
    verbose: bool,
    compiler: Option<&Path>,
) -> Result<ProbeResult, String> {
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

    let mut cmd = Command::new(&bin);
    cmd.args(["bluedb-gates", "--mutation-probe", &format!("--only={gate_id}")])
        .current_dir(&guard.wt);
    // The TOOL may come from outside the worktree; the SUBJECT never does. See
    // `mutation_touches_compiler` for why that is safe here and refused there.
    if let Some(c) = compiler {
        cmd.env("SKY_BLUEDB_COMPILER", c);
    }
    let out = cmd.output().map_err(|e| format!("probe run: {e}"))?;

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
    // The probe measures HEAD. Refuse before spending an hour measuring code
    // the developer did not write — see `head_skew`.
    let skew = head_skew(root);
    if !skew.is_empty() {
        return Err(format!(
            "the working tree differs from HEAD in {} path(s) the mutation probe measures:\n{}\n\n\
             The probe runs in a `git worktree add --detach HEAD`, so it compiles and reads \
             the last COMMIT — none of the above. The parent process that classifies its \
             output is the binary you just built from these files, so the two would run \
             different code and the verdict would describe neither. Commit (or stash) them \
             and re-run.",
            skew.len(),
            skew.iter()
                .map(|l| format!("  {l}"))
                .collect::<Vec<_>>()
                .join("\n")
        ));
    }

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

    // A gate that builds something needs a compiler, and the scratch worktree
    // has none. Lend the dev tree's — but only when the mutation is not IN the
    // compiler, or the proof would be measuring an unmutated tool.
    let compiler = if mutation_touches_compiler(root, m) {
        None
    } else {
        dev_tree_compiler(root)
    };

    // --- baseline, measured in the worktree ------------------------------
    let base = match probe(guard, gate_id, verbose, compiler.as_deref()) {
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
    let red = match probe(guard, gate_id, verbose, compiler.as_deref()) {
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
    let _ = std::fs::write(&expected, normalise(&red.output, guard));

    Some(ProofOutcome::Proven)
}

/// Recorded RED outputs are committed artefacts, so they must be
/// **deterministic**: the scratch worktree path is unique per run, and leaving
/// it in the file made every proof anchored on it go `MUTATION-STALE` on the
/// next run — and leaked a developer's absolute paths into the repo.
fn normalise(output: &str, guard: &WorktreeGuard) -> String {
    let mut s = output.to_string();
    for p in [
        std::fs::canonicalize(&guard.wt).unwrap_or_else(|_| guard.wt.clone()),
        guard.wt.clone(),
    ] {
        s = s.replace(&p.to_string_lossy().to_string(), "<scratch-worktree>");
    }
    for p in [
        std::fs::canonicalize(&guard.scratch).unwrap_or_else(|_| guard.scratch.clone()),
        guard.scratch.clone(),
    ] {
        s = s.replace(&p.to_string_lossy().to_string(), "<scratch-root>");
    }
    s
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
    fn porcelain_paths_survive_renames_quotes_and_untracked() {
        assert_eq!(
            porcelain_path(" M rust/crates/xtask/src/a.rs").unwrap(),
            "rust/crates/xtask/src/a.rs"
        );
        assert_eq!(
            porcelain_path("?? docs/bluedb/x.expected.txt").unwrap(),
            "docs/bluedb/x.expected.txt"
        );
        assert_eq!(
            porcelain_path("R  docs/a.md -> docs/b.md").unwrap(),
            "docs/b.md"
        );
        assert_eq!(
            porcelain_path("A  \"docs/with space.md\"").unwrap(),
            "docs/with space.md"
        );
        assert_eq!(porcelain_path(""), None);
    }

    /// The runner writes `gate-state.tsv` and `*.expected.txt` into the dev tree
    /// AS IT RUNS, and reads `*.patch` from there by design. If those counted as
    /// skew, the first run would poison the second and `--verify-mutations`
    /// could never be run twice.
    #[test]
    fn the_runners_own_artefacts_are_not_head_skew() {
        assert!(read_from_the_dev_tree("docs/bluedb/gate-state.tsv"));
        assert!(read_from_the_dev_tree(
            "docs/bluedb/mutations/G0.3.persistglue-unconditional.expected.txt"
        ));
        assert!(read_from_the_dev_tree(
            "docs/bluedb/mutations/G0.3.persistglue-unconditional.patch"
        ));
        // …but the gate bodies, the runtime, the stdlib and the witnesses are
        // measured from HEAD, so a working-tree-only edit to any of them is the
        // skew that made G0.3's proof read VACUOUS for a whole session.
        assert!(!read_from_the_dev_tree(
            "rust/crates/xtask/src/bluedb_gates/gates_g0.rs"
        ));
        assert!(!read_from_the_dev_tree("runtime-go/rt/rt.go"));
        assert!(!read_from_the_dev_tree("docs/bluedb/v2-architecture.md"));
    }

    /// A `git status` we cannot run is skew, not freshness — the same rule
    /// `targets_moved` applies to an unresolvable sha.
    #[test]
    fn an_unrunnable_git_status_counts_as_skew() {
        assert!(!head_skew(Path::new("/nonexistent-bluedb-root")).is_empty());
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
