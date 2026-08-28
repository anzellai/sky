//! The falsifier runner — "does this gate's assertion actually bite?"
//!
//! A gate that cannot fail is worse than no gate, because it consumes the
//! budget of a real one while certifying nothing. The audit behind this mandate
//! found eleven of them at once. So every gate declares a mutation, and this
//! runner proves the mutation makes it red.
//!
//! The protocol, per gate × mutation:
//!
//! ```text
//!   1. baseline run          → must PASS, else INCONCLUSIVE
//!                              ("both sides failing proves nothing", v2 §4.2)
//!   2. apply the mutation    → exact-once textual replacement, guarded
//!   3. run again             → FAIL ⇒ PROVEN, PASS ⇒ VACUOUS
//!   4. revert                → unconditional, including on panic
//! ```
//!
//! Two properties the BlueDB precedent lacks and that are load-bearing here:
//!
//! * **The mutation probe is timed out.** The precedent runs `cargo build` with
//!   no timeout at all. Here the mutated run is supervised by the same
//!   `killpg`-backed budget as the baseline.
//! * **The revert is guaranteed.** [`Patch`] restores in `Drop`, so a panic
//!   between apply and revert cannot leave a mutated source in the tree.

use super::child::{result_path, run_gate_in_child};
use super::registry::{Expect, Gate, Mutation, MutationKind};
use std::path::{Path, PathBuf};
use std::time::Duration;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Falsified {
    /// The gate went red under its mutation. The assertion is live.
    Proven,
    /// The gate stayed green under its mutation. For a normal gate this is a
    /// **defect**; for the canary it is the **required** answer.
    Vacuous,
    /// Nothing could be concluded — the baseline was not green, the mutation
    /// could not be applied, or the run could not be supervised.
    ///
    /// Deliberately distinct from both: "I could not tell" must never be
    /// rounded to "proven", and rounding it to "vacuous" would blame the gate
    /// for the harness's own inability to run.
    Inconclusive(String),
}

impl Falsified {
    pub fn label(&self) -> &'static str {
        match self {
            Falsified::Proven => "PROVEN",
            Falsified::Vacuous => "VACUOUS",
            Falsified::Inconclusive(_) => "INCONCLUSIVE",
        }
    }
}

pub struct FalsifyReport {
    pub gate: &'static str,
    pub mutation: &'static str,
    pub outcome: Falsified,
    /// Did the observed outcome match the gate's declared [`Expect`]?
    ///
    /// For every gate but the canary this is `outcome == Proven`. For the
    /// canary it is `outcome == Vacuous` — the one place where a *passing*
    /// gate is the success signal, and where `PROVEN` means the harness itself
    /// is broken.
    pub as_declared: bool,
    pub detail: String,
}

/// The crash-recovery journal.
///
/// `Drop` restores on a panic, but it does **not** run when the process is
/// killed by a signal — and this runner exists to be killed: the harness enforces
/// budgets with `killpg`, CI cancels jobs, and operators interrupt long runs.
/// A run killed between apply and revert leaves a **mutated source file in the
/// working tree**, and the next run then measures the mutation instead of the
/// change under test.
///
/// That is not hypothetical. It happened during Phase 3 (2026-08-10): an
/// interrupted `--verify-falsifiers` left `MathConformanceTest.sky` mutated, and
/// the next two `harness` runs reported a `632/770` conformance count and a
/// spurious `min 3 7 == 3 … expected 4 but got 3` failure. Two runs were spent
/// chasing a compiler regression that did not exist.
///
/// So the original content is journalled to disk **before** the file is touched,
/// and [`restore_orphans`] replays any journal left behind before the next run
/// starts. The journal is removed on a clean revert, so its mere presence is the
/// signal that a previous run died mid-mutation.
const JOURNAL_DIR: &str = ".skycache/harness/mutation-journal";

/// Replay any mutation journal left by a previous run that died between apply
/// and revert. Returns the files restored, so the caller can say so out loud
/// rather than silently repairing.
pub fn restore_orphans(root: &Path) -> Vec<String> {
    let dir = root.join(JOURNAL_DIR);
    let mut restored = Vec::new();
    let Ok(rd) = std::fs::read_dir(&dir) else {
        return restored;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for entry in entries {
        if entry.extension().and_then(|e| e.to_str()) != Some("journal") {
            continue;
        }
        let Ok(blob) = std::fs::read_to_string(&entry) else {
            continue;
        };
        // Format: the relative path, a newline, then the original bytes.
        let Some((rel, original)) = blob.split_once('\n') else {
            let _ = std::fs::remove_file(&entry);
            continue;
        };
        let target = root.join(rel);
        if std::fs::write(&target, original).is_ok() {
            restored.push(rel.to_string());
        }
        let _ = std::fs::remove_file(&entry);
    }
    restored
}

/// A textual mutation applied to the working tree, reverted on drop **and**
/// journalled to disk so a signal-kill cannot leave it applied.
#[derive(Debug)]
struct Patch {
    path: PathBuf,
    original: String,
    /// The mutation target's mtime *before* we touched it. The revert restores
    /// byte-identical content, so the file is genuinely unchanged — and its
    /// freshness timestamp must be too. Without this, `fs::write` in `revert`
    /// stamps the file with "now", and a mutation on an embed-root source
    /// (`runtime-go/`, `sky-stdlib/`) makes `sky-out/sky` look STALE to a
    /// *sibling* gate that runs later and carries a fresh-compiler guard
    /// (`sky-suites`). That surfaced as a spurious INCONCLUSIVE — the harness
    /// perturbing its own downstream verdict.
    original_mtime: Option<std::time::SystemTime>,
    applied: bool,
    journal: Option<PathBuf>,
}

impl Patch {
    /// Apply an exact-once replacement.
    ///
    /// Refuses when the pattern is absent (the mutation has rotted — this is
    /// how "7 of 48 verified" happens) or occurs more than once (the mutation
    /// is ambiguous and might perturb something other than the axis under
    /// test).
    fn apply(root: &Path, rel: &str, from: &str, to: &str) -> Result<Patch, String> {
        let path = root.join(rel);
        let original = std::fs::read_to_string(&path)
            .map_err(|e| format!("cannot read mutation target {rel}: {e}"))?;
        // Captured before the write so the revert can put the freshness clock
        // back exactly where it was — see the field docstring.
        let original_mtime = std::fs::metadata(&path).ok().and_then(|m| m.modified().ok());
        let hits = original.matches(from).count();
        if hits != 1 {
            return Err(format!(
                "mutation pattern {from:?} occurs {hits}x in {rel} (must be exactly 1)"
            ));
        }
        let mutated = original.replacen(from, to, 1);

        // Journal BEFORE touching the file — a crash between the write and the
        // journal would be exactly the hole this closes.
        let jdir = root.join(JOURNAL_DIR);
        let _ = std::fs::create_dir_all(&jdir);
        let jpath = jdir.join(format!("{}.journal", rel.replace(['/', '\\'], "_")));
        let journal = match std::fs::write(&jpath, format!("{rel}\n{original}")) {
            Ok(()) => Some(jpath),
            // A journal we cannot write is a refusal, not a warning: proceeding
            // would reintroduce the "killed run poisons the tree" class.
            Err(e) => {
                return Err(format!(
                    "cannot journal mutation target {rel} ({e}); refusing to mutate \
                     a tree we could not guarantee restoring"
                ))
            }
        };

        if let Err(e) = std::fs::write(&path, &mutated) {
            if let Some(j) = &journal {
                let _ = std::fs::remove_file(j);
            }
            return Err(format!("cannot write mutation to {rel}: {e}"));
        }
        Ok(Patch {
            path,
            original,
            original_mtime,
            applied: true,
            journal,
        })
    }

    fn revert(&mut self) {
        if self.applied {
            let _ = std::fs::write(&self.path, &self.original);
            // Content is byte-identical to before the mutation, so the file is
            // genuinely unchanged — put its mtime back too, or a sibling gate's
            // fresh-compiler guard reads this self-inflicted "now" stamp as a
            // stale `sky-out/sky` and refuses a verdict (the sky-suites
            // INCONCLUSIVE). Best-effort: a failure here only reintroduces the
            // benign timestamp bump, never corrupts content.
            if let Some(mt) = self.original_mtime {
                if let Ok(f) = std::fs::OpenOptions::new().write(true).open(&self.path) {
                    let _ = f.set_modified(mt);
                }
            }
            self.applied = false;
            // Restoring the SOURCE is not enough. The mutated run may have
            // emitted Go and built a binary FROM the mutation, and those
            // artifacts outlive the source restore — leaving an example whose
            // `sky-out/` disagrees with its own `.sky` file.
            //
            // This is the same defect the journal above exists to prevent, one
            // layer down, and it cost a release: `01-hello-world` printed
            // "Goodbye from Sky!" from a stale `sky-out/main.go` while its
            // source read "Hello from Sky!" and git reported the tree clean.
            // `preflight-tag.sh` failed the build-run gate on an oracle
            // divergence that did not exist. The dangerous version of this is
            // the mirror image: a stale artefact that happens to match, letting
            // a gate PASS over a compiler that never produced it.
            discard_build_artifacts(&self.path);
        }
        // Only after the content is back: the journal's presence means "a
        // mutation may still be applied", so it must outlive the restore.
        if let Some(j) = self.journal.take() {
            let _ = std::fs::remove_file(j);
        }
    }
}

/// Delete the build output of the project a mutated file belongs to, so the
/// next gate re-emits from the restored source instead of reading what the
/// mutation produced.
///
/// Walks up from the mutated file to the nearest directory holding a
/// `sky.toml`, and removes that project's generated trees. Deleting them is
/// safe by construction: both are build artefacts, both are gitignored, and the
/// gates that need them rebuild them. Doing nothing is what is unsafe.
fn discard_build_artifacts(mutated: &Path) {
    let mut dir = mutated.parent();
    while let Some(d) = dir {
        if d.join("sky.toml").is_file() {
            for generated in ["sky-out", ".skycache"] {
                let p = d.join(generated);
                if p.exists() {
                    let _ = std::fs::remove_dir_all(&p);
                }
            }
            return;
        }
        dir = d.parent();
    }
}

impl Drop for Patch {
    fn drop(&mut self) {
        // Unconditional. A panic between apply and revert must not leave a
        // mutated source behind for the next gate — or for the developer.
        self.revert();
    }
}

pub struct FalsifyOpts {
    pub exe: PathBuf,
    pub repo_root: PathBuf,
    pub scratch: PathBuf,
}

/// Verify one gate's declared mutations.
pub fn verify_gate(gate: &'static Gate, opts: &FalsifyOpts, generation: &mut u64) -> Vec<FalsifyReport> {
    let budget = Duration::from_secs(gate.budget_s);
    let mut out = Vec::new();

    // ---- step 1: the baseline must be green -------------------------------
    //
    // "Both sides failing identically proves nothing" (v2 §4.2) generalises: a
    // gate that is already red tells us nothing about whether the MUTATION made
    // it red. Refusing here is what stops a broken tree from manufacturing a
    // wall of false PROVENs.
    *generation += 1;
    let base_gen = *generation;
    let base = run_gate_in_child(
        &opts.exe,
        &opts.repo_root,
        gate.name,
        base_gen,
        budget,
        &result_path(&opts.scratch, gate.name, base_gen),
    );
    let base_green = !base.timed_out
        && base
            .result
            .as_ref()
            .map(|r| r.passed && r.assertions > 0)
            .unwrap_or(false);

    if !base_green {
        let why = if base.timed_out {
            format!("baseline exceeded its {}s budget", gate.budget_s)
        } else {
            match &base.result {
                Some(r) => format!("baseline is red: {}", r.detail),
                None => "baseline produced no result".to_string(),
            }
        };
        for m in gate.mutations.as_slice() {
            out.push(FalsifyReport {
                gate: gate.name,
                mutation: m.id,
                outcome: Falsified::Inconclusive(why.clone()),
                as_declared: false,
                detail: why.clone(),
            });
        }
        return out;
    }

    // ---- steps 2-4: one mutation at a time --------------------------------
    for m in gate.mutations.as_slice() {
        *generation += 1;
        out.push(verify_one(gate, m, opts, *generation, budget));
    }
    out
}

fn verify_one(
    gate: &'static Gate,
    m: &'static Mutation,
    opts: &FalsifyOpts,
    generation: u64,
    budget: Duration,
) -> FalsifyReport {
    // The patch guard lives for exactly the mutated run.
    let _patch = match m.kind {
        MutationKind::ReplaceOnce { path, from, to } => {
            match Patch::apply(&opts.repo_root, path, from, to) {
                Ok(p) => Some(p),
                Err(e) => {
                    return FalsifyReport {
                        gate: gate.name,
                        mutation: m.id,
                        outcome: Falsified::Inconclusive(e.clone()),
                        as_declared: false,
                        detail: e,
                    };
                }
            }
        }
        // The canary. Nothing is written; the tree is byte-identical.
        MutationKind::NoOp => None,
    };

    // A mutation to RUST SOURCE does nothing until the binary is rebuilt — the
    // child re-execs `opts.exe`, which is the pre-mutation image. Without this
    // step such a mutation is a silent no-op and the gate reports VACUOUS,
    // indistinguishable from a gate whose assertion is genuinely dead.
    //
    // Every gate registered before 2026-08-10 happened to mutate a DATA file
    // (a corpus `.sky`, an example, a test suite), so the hole never showed. The
    // Layer-1 corpus gates are driven by generator logic in Rust, so it shows
    // immediately. v2 §7.5 already costs falsification at "~13-24 s cold build
    // per mutation" — it assumed this rebuild; the harness had not implemented
    // it.
    if let MutationKind::ReplaceOnce { path, .. } = m.kind {
        if path.ends_with(".rs") {
            if let Err(e) = rebuild_xtask(&opts.repo_root, budget) {
                return FalsifyReport {
                    gate: gate.name,
                    mutation: m.id,
                    outcome: Falsified::Inconclusive(e.clone()),
                    as_declared: false,
                    detail: e,
                };
            }
        }
    }

    let run = run_gate_in_child(
        &opts.exe,
        &opts.repo_root,
        gate.name,
        generation,
        budget,
        &result_path(&opts.scratch, gate.name, generation),
    );

    // A mutated run that times out IS red — the mutation broke it badly enough
    // to hang. That is a genuine falsification, and it is bounded, unlike the
    // precedent's untimed mutation probe.
    // NEGATIVE CONTROL, run 2026-08-09: hard-coding `went_red = true` here —
    // a runner whose every answer is "it went red" — makes the canary report
    // PROVEN, and the canary is the ONLY gate that notices, failing the whole
    // falsifier run. Every other gate's PROVEN looks identical either way.
    let went_red = run.timed_out
        || run
            .result
            .as_ref()
            .map(|r| !r.passed || r.assertions == 0)
            .unwrap_or(true);

    let outcome = if went_red {
        Falsified::Proven
    } else {
        Falsified::Vacuous
    };

    let as_declared = match gate.expect {
        Expect::Falsifiable => outcome == Falsified::Proven,
        Expect::Vacuous => outcome == Falsified::Vacuous,
    };

    let detail = match (&outcome, gate.expect) {
        (Falsified::Proven, Expect::Falsifiable) => run
            .result
            .as_ref()
            .map(|r| format!("went red as declared: {}", r.detail))
            .unwrap_or_else(|| "went red (no result — timed out or crashed)".into()),
        (Falsified::Vacuous, Expect::Vacuous) => {
            "stayed green under a no-op patch, as a correct runner must".into()
        }
        (Falsified::Vacuous, Expect::Falsifiable) => {
            "STAYED GREEN under its mutation — this gate asserts nothing about the axis \
             it claims to cover"
                .into()
        }
        (Falsified::Proven, Expect::Vacuous) => {
            "CANARY WENT RED under a NO-OP patch — the harness is broken: it is reporting \
             falsification for a change that was never made (patch applied in the wrong \
             tree, or the verdict is not being read from this run)"
                .into()
        }
        (Falsified::Inconclusive(w), _) => w.clone(),
    };

    // Restore the tree NOW, then rebuild, so the next gate does not run a
    // binary built from mutated source. Reverting the FILE is not enough once a
    // mutation can reach the compiled image: the artefact outlives the patch.
    let rebuilt_source = matches!(
        m.kind,
        MutationKind::ReplaceOnce { path, .. } if path.ends_with(".rs")
    );
    drop(_patch);
    if rebuilt_source {
        if let Err(e) = rebuild_xtask(&opts.repo_root, budget) {
            eprintln!(
                "harness: WARNING — could not rebuild after reverting {}: {e}\n\
                 The binary may still contain the mutation. Rebuild before trusting \
                 any later result.",
                m.id
            );
        }
    }

    FalsifyReport {
        gate: gate.name,
        mutation: m.id,
        outcome,
        as_declared,
        detail,
    }
}

/// Rebuild the `xtask` binary in place, bounded.
///
/// Used when a mutation edits Rust source: the child re-execs the binary at
/// `opts.exe`, so the mutation only exists once it has been compiled into that
/// path.
fn rebuild_xtask(root: &Path, budget: Duration) -> Result<(), String> {
    use std::process::{Command, Stdio};

    let mut cmd = Command::new("cargo");
    cmd.args(["build", "--release", "-p", "xtask"])
        .current_dir(root.join("rust"))
        // The harness is itself usually invoked under a wrapping CARGO_TARGET_DIR;
        // inheriting it here would build into a different tree than the one
        // `opts.exe` points at, and the mutation would silently not take effect
        // — the exact failure this function exists to remove.
        .env_remove("CARGO_TARGET_DIR")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped());
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        cmd.process_group(0);
    }

    let mut child = cmd.spawn().map_err(|e| format!("cargo spawn failed: {e}"))?;
    let deadline = std::time::Instant::now() + budget;
    loop {
        match child.try_wait() {
            Ok(Some(st)) => {
                if st.success() {
                    return Ok(());
                }
                let mut err = String::new();
                if let Some(mut s) = child.stderr.take() {
                    use std::io::Read;
                    let _ = s.read_to_string(&mut err);
                }
                let tail: Vec<&str> = err.lines().rev().take(8).collect();
                return Err(format!(
                    "cargo build failed under the mutation: {}",
                    tail.into_iter().rev().collect::<Vec<_>>().join(" | ")
                ));
            }
            Ok(None) => {
                if std::time::Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    return Err("cargo build exceeded the gate's budget".into());
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(e) => return Err(format!("cargo wait failed: {e}")),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!(
            "sky-falsify-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn a_patch_reverts_on_drop() {
        let root = tmp("revert");
        std::fs::write(root.join("f.txt"), "hello world").unwrap();
        {
            let _p = Patch::apply(&root, "f.txt", "world", "mutant").unwrap();
            assert_eq!(
                std::fs::read_to_string(root.join("f.txt")).unwrap(),
                "hello mutant"
            );
        }
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "hello world",
            "the tree must be restored when the guard drops"
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn a_patch_reverts_even_when_the_scope_panics() {
        let root = tmp("panic");
        std::fs::write(root.join("f.txt"), "hello world").unwrap();
        let r = root.clone();
        let _ = std::panic::catch_unwind(move || {
            let _p = Patch::apply(&r, "f.txt", "world", "mutant").unwrap();
            panic!("simulated failure mid-mutation");
        });
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "hello world",
            "a panic between apply and revert must not leave a mutated tree"
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    /// The signal-kill case `Drop` cannot cover, and the one that actually bit
    /// (Phase 3, 2026-08-10: an interrupted falsifier run left a conformance
    /// suite mutated and two later harness runs reported a failure that did not
    /// exist). `std::mem::forget` models the kill exactly: the guard's `Drop`
    /// never runs, the file stays mutated — and the journal is what gets it back.
    #[test]
    fn a_journal_restores_a_mutation_whose_guard_never_dropped() {
        let root = tmp("orphan");
        std::fs::write(root.join("f.txt"), "hello world").unwrap();

        let p = Patch::apply(&root, "f.txt", "world", "mutant").unwrap();
        std::mem::forget(p); // the process was killed: no Drop, no revert
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "hello mutant",
            "precondition: the tree really is left mutated"
        );

        let restored = restore_orphans(&root);
        assert_eq!(restored, vec!["f.txt".to_string()]);
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "hello world",
            "the journal must restore a mutation no Drop ever reverted"
        );

        // Replaying an empty journal is a no-op, so recovery cannot itself
        // clobber a later legitimate edit.
        std::fs::write(root.join("f.txt"), "edited since").unwrap();
        assert!(restore_orphans(&root).is_empty());
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "edited since"
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    /// A clean revert must leave no journal behind — otherwise every subsequent
    /// run would "recover" from a mutation that was already undone.
    #[test]
    fn a_clean_revert_leaves_no_journal() {
        let root = tmp("nojournal");
        std::fs::write(root.join("f.txt"), "hello world").unwrap();
        {
            let _p = Patch::apply(&root, "f.txt", "world", "mutant").unwrap();
        }
        std::fs::write(root.join("f.txt"), "edited since").unwrap();
        assert!(restore_orphans(&root).is_empty());
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "edited since",
            "a stale journal clobbered a later edit"
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    /// Reverting a mutated source must also discard what the mutated run BUILT.
    ///
    /// Restoring only the source leaves an example whose `sky-out/` was emitted
    /// from the mutation. That is not theoretical: `01-hello-world` printed
    /// "Goodbye from Sky!" from a stale `sky-out/main.go` while its source read
    /// "Hello from Sky!" and git called the tree clean — failing the release
    /// preflight on an oracle divergence that did not exist. The mirror image
    /// is worse: a stale artefact that happens to MATCH lets a gate pass over a
    /// compiler that never produced it.
    #[test]
    fn a_revert_discards_artifacts_built_from_the_mutation() {
        let root = tmp("artifacts");
        let proj = root.join("examples/ex");
        std::fs::create_dir_all(proj.join("src")).unwrap();
        std::fs::write(proj.join("sky.toml"), "name = \"ex\"\n").unwrap();
        std::fs::write(proj.join("src/Main.sky"), "println \"hello\"\n").unwrap();

        {
            let _p = Patch::apply(&root, "examples/ex/src/Main.sky", "hello", "goodbye").unwrap();
            // Stand in for what a mutated run emits and builds.
            std::fs::create_dir_all(proj.join("sky-out")).unwrap();
            std::fs::write(proj.join("sky-out/main.go"), "goodbye").unwrap();
            std::fs::create_dir_all(proj.join(".skycache")).unwrap();
        }

        assert_eq!(
            std::fs::read_to_string(proj.join("src/Main.sky")).unwrap(),
            "println \"hello\"\n",
            "source was not restored"
        );
        assert!(
            !proj.join("sky-out").exists(),
            "sky-out/ survived the revert — the next gate would read Go emitted \
             from the mutation"
        );
        assert!(!proj.join(".skycache").exists(), ".skycache/ survived");
        // The project itself must not be collateral damage.
        assert!(proj.join("sky.toml").is_file());
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn a_mutation_whose_pattern_is_missing_is_refused() {
        // The "7 of 48 verified" failure mode: a literal gets reworded and the
        // mutation silently stops mutating. Refusing beats reporting VACUOUS.
        let root = tmp("missing");
        std::fs::write(root.join("f.txt"), "hello world").unwrap();
        let e = Patch::apply(&root, "f.txt", "absent", "x").unwrap_err();
        assert!(e.contains("occurs 0x"), "{e}");
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "hello world"
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn an_ambiguous_mutation_is_refused() {
        let root = tmp("ambiguous");
        std::fs::write(root.join("f.txt"), "a a").unwrap();
        let e = Patch::apply(&root, "f.txt", "a", "b").unwrap_err();
        assert!(e.contains("occurs 2x"), "{e}");
        let _ = std::fs::remove_dir_all(&root);
    }
}
