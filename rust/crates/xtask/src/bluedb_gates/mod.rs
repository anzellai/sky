//! `xtask bluedb-gates` — the RULE ZERO executable-state harness (§9 of
//! `docs/bluedb/v2-architecture.md`).
//!
//! ```text
//! cargo run -p xtask -- bluedb-gates                    # fast tier; regenerates STATUS.md
//! cargo run -p xtask -- bluedb-gates --only=G0.2        # one gate — NEVER writes STATUS.md
//! cargo run -p xtask -- bluedb-gates --json             # machine-readable
//! cargo run -p xtask -- bluedb-gates --check            # verify STATUS.md matches a fresh run
//! cargo run -p xtask -- bluedb-gates --verify-mutations # apply every recorded mutation, assert RED
//! cargo run -p xtask -- bluedb-gates --tier=full        # the only invocation that can clear STALE
//! cargo run -p xtask -- bluedb-gates --bless            # update baselines.json
//! ```
//!
//! The countermeasure to "a fresh or compacted session inherits CLAIMS": goal
//! status is COMPUTED by running the gates, never read from prose.

pub mod frozen_stage1;
pub mod gates_g0;
pub mod gates_g2;
pub mod gates_g2_13;
pub mod gates_runtime;
pub mod mutations;
pub mod pending;
pub mod registry;
pub mod sha256;
pub mod state;
pub mod status;

use std::collections::BTreeMap;
use std::path::Path;
use std::time::Instant;

use registry::{Ctx, Gate, GateOutcome, GateState, GoalVerdict, Tier, REGISTRY};
use status::{Header, Row};

pub fn run(args: &[String], root: &Path) -> i32 {
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");
    let json = args.iter().any(|a| a == "--json");
    let check = args.iter().any(|a| a == "--check");
    let bless = args.iter().any(|a| a == "--bless");
    let verify_mutations = args.iter().any(|a| a == "--verify-mutations");
    let mutation_probe = args.iter().any(|a| a == "--mutation-probe");
    let only: Option<String> = args
        .iter()
        .find_map(|a| a.strip_prefix("--only=").map(|s| s.to_string()));
    let tier = if args.iter().any(|a| a == "--tier=full") {
        Tier::Full
    } else {
        Tier::Fast
    };

    if let Some(id) = &only {
        if registry::find(id).is_none() {
            eprintln!("bluedb-gates: no gate `{id}` in the registry");
            return 2;
        }
    }

    if bless {
        return bless_baselines(root);
    }

    if verify_mutations {
        return run_verify_mutations(root, verbose, only.as_deref());
    }

    if mutation_probe {
        return run_mutation_probe(root, only.as_deref(), verbose);
    }

    // ---- the three static checks, before any gate executes (§9.6) -------
    let static_findings = gates_g0::static_checks();
    if !static_findings.is_empty() {
        eprintln!("bluedb-gates: HARNESS SELF-INTEGRITY FAILURES\n");
        for f in &static_findings {
            eprintln!("  {f}");
        }
        eprintln!();
    }

    let snapshot = std::fs::read_to_string(root.join(status::STATUS_PATH)).ok();
    let results = execute(root, tier, only.as_deref(), verbose, snapshot);

    let ledger = state::GateState::load(root);
    let rows = build_rows(root, &results, &ledger);

    if json {
        print!("{}", render_json(&rows));
    } else {
        print_rollup(&rows, tier, only.is_some());
    }

    let head = mutations::head_sha(root);
    let full_behind = full_tier_behind(root, &ledger);
    let header = Header {
        commit: head.clone(),
        ran: now_utc(),
        host: format!("{}/{}", std::env::consts::OS, std::env::consts::ARCH),
        tier,
        full_tier: ledger
            .full_tier
            .as_ref()
            .map(|f| (f.commit.clone(), f.ran.clone(), f.host.clone())),
        full_tier_behind: full_behind,
    };
    let rendered = status::render(&header, &rows);

    if check {
        return run_check(root, &rendered, &head, &ledger);
    }

    // §9.1 — `--only` may not author STATUS.md. A partial run that regenerated
    // the file would report every other gate as whatever the schema does with
    // absent outcomes; the cleaner rule is that it cannot write at all.
    if only.is_some() {
        println!(
            "\n(--only: STATUS.md NOT written — a partial run may not author the status file)"
        );
    } else {
        if let Err(e) = write_status(root, &rendered) {
            eprintln!("bluedb-gates: could not write {}: {e}", status::STATUS_PATH);
            return 2;
        }
        // Only `--tier=full` may advance the full-tier clock (§9.3 property 4).
        if tier == Tier::Full {
            let mut l = ledger.clone();
            l.full_tier = Some(state::FullTierRun {
                commit: head.clone(),
                ran: now_utc(),
                host: format!("{}/{}", std::env::consts::OS, std::env::consts::ARCH),
            });
            if let Err(e) = l.save(root) {
                eprintln!("bluedb-gates: could not write the gate-state ledger: {e}");
                return 2;
            }
            // Re-render so the header carries the clock this run just set.
            let header = Header {
                commit: head.clone(),
                ran: now_utc(),
                host: format!("{}/{}", std::env::consts::OS, std::env::consts::ARCH),
                tier,
                full_tier: l
                    .full_tier
                    .as_ref()
                    .map(|f| (f.commit.clone(), f.ran.clone(), f.host.clone())),
                full_tier_behind: Some(0),
            };
            let rendered = status::render(&header, &rows);
            let _ = write_status(root, &rendered);
        }
    }

    let failed = rows.iter().any(|r| r.state == GateState::Fail);
    let unknown = goal_verdicts(&rows)
        .values()
        .any(|v| *v == GoalVerdict::Unknown);

    if !static_findings.is_empty() || failed || unknown {
        1
    } else {
        0
    }
}

// ---------------------------------------------------------------------------
// execution
// ---------------------------------------------------------------------------

struct Executed {
    state: GateState,
    secs: Option<f64>,
    detail: String,
    findings: Vec<String>,
}

fn execute(
    root: &Path,
    tier: Tier,
    only: Option<&str>,
    verbose: bool,
    snapshot: Option<String>,
) -> BTreeMap<&'static str, Executed> {
    let mut out = BTreeMap::new();
    for gate in REGISTRY {
        // H1's runtime backstop: a gate that reached runtime with no mutation
        // is UNPROVEN, which makes its goal FAIL. It is not executed — its
        // result would be meaningless.
        if gate.mutations.is_empty() {
            out.insert(
                gate.id,
                Executed {
                    state: GateState::Unproven,
                    secs: None,
                    detail: "no mutation declared — nobody ever tried to falsify this gate".into(),
                    findings: vec![],
                },
            );
            continue;
        }

        let selected = match only {
            Some(id) => gate.id == id,
            None => gate.tier == Tier::Fast || tier == Tier::Full,
        };
        if !selected {
            let reason = if only.is_some() {
                "not selected by --only".to_string()
            } else {
                format!("tier {} — run with --tier=full", gate.tier.label())
            };
            out.insert(
                gate.id,
                Executed {
                    state: GateState::NotRun,
                    secs: None,
                    detail: reason,
                    findings: vec![],
                },
            );
            continue;
        }

        out.insert(gate.id, run_one(root, gate, tier, verbose, snapshot.clone()));
    }
    out
}

fn run_one(
    root: &Path,
    gate: &'static Gate,
    tier: Tier,
    verbose: bool,
    snapshot: Option<String>,
) -> Executed {
    let ctx = Ctx::new(root.to_path_buf(), tier, verbose, snapshot);
    let started = Instant::now();

    // `budget_s` is a hard timeout: exceeding it is a FAIL, not a hang.
    let (tx, rx) = std::sync::mpsc::channel();
    let run = gate.run;
    std::thread::spawn(move || {
        let _ = tx.send(run(&ctx));
    });

    let outcome = match rx.recv_timeout(std::time::Duration::from_secs(gate.budget_s)) {
        Ok(o) => o,
        Err(_) => GateOutcome::fail(
            format!("exceeded its {}s budget", gate.budget_s),
            vec!["a gate that runs past its budget is a FAIL, not a hang".into()],
        ),
    };
    let secs = started.elapsed().as_secs_f64();

    match outcome {
        GateOutcome::Pass { detail } => Executed {
            state: GateState::Pass,
            secs: Some(secs),
            detail,
            findings: vec![],
        },
        GateOutcome::Fail { detail, findings } => Executed {
            state: GateState::Fail,
            secs: Some(secs),
            detail,
            findings,
        },
        GateOutcome::NotRun { reason } => Executed {
            state: GateState::NotRun,
            secs: None,
            detail: reason,
            findings: vec![],
        },
    }
}

/// §9.3 property 1 — rows come from the **registry**, so every registered gate
/// appears whether or not it ran.
fn build_rows(
    root: &Path,
    results: &BTreeMap<&'static str, Executed>,
    ledger: &state::GateState,
) -> Vec<Row> {
    REGISTRY
        .iter()
        .map(|g| {
            let r = results.get(g.id);
            let ids: Vec<&str> = g.mutations.as_slice().iter().map(|m| m.id).collect();
            let targets: BTreeMap<&str, &[&str]> = g
                .mutations
                .as_slice()
                .iter()
                .map(|m| (m.id, m.targets))
                .collect();
            let moved = |id: &str, sha: &str| -> bool {
                targets
                    .get(id)
                    .map(|t| mutations::targets_moved(root, sha, t))
                    .unwrap_or(true)
            };
            let (proof, proof_unknown) = status::proof_cell(g.id, &ids, &ledger.proofs, &moved);
            Row {
                id: g.id,
                goal: g.goal,
                title: g.title,
                tier: g.tier,
                state: r.map(|r| r.state).unwrap_or(GateState::NotRun),
                secs: r.and_then(|r| r.secs),
                detail: r.map(|r| r.detail.clone()).unwrap_or_default(),
                findings: r.map(|r| r.findings.clone()).unwrap_or_default(),
                proof,
                proof_unknown,
            }
        })
        .collect()
}

fn goal_verdicts(rows: &[Row]) -> BTreeMap<u8, GoalVerdict> {
    let mut by_goal: BTreeMap<u8, Vec<(GateState, bool)>> = BTreeMap::new();
    for r in rows {
        by_goal
            .entry(r.goal)
            .or_default()
            .push((r.state, r.proof_unknown));
    }
    by_goal
        .into_iter()
        .map(|(g, s)| (g, registry::goal_verdict(&s)))
        .collect()
}

// ---------------------------------------------------------------------------
// sub-commands
// ---------------------------------------------------------------------------

fn run_check(root: &Path, fresh: &str, head: &str, ledger: &state::GateState) -> i32 {
    let Ok(on_disk) = std::fs::read_to_string(root.join(status::STATUS_PATH)) else {
        eprintln!(
            "bluedb-gates --check: {} is absent. STATUS.md is generated output; run `cargo run -p xtask -- bluedb-gates`",
            status::STATUS_PATH
        );
        return 1;
    };

    let mut bad = false;

    match status::split_body_and_sha(&on_disk) {
        None => {
            eprintln!("bluedb-gates --check: {} has no body-sha256 trailer; hand edits would be undetectable", status::STATUS_PATH);
            bad = true;
        }
        Some((body, recorded)) => {
            if sha256::hex(body.as_bytes()) != recorded {
                eprintln!(
                    "bluedb-gates --check: body-sha256 mismatch — STATUS.md is generated output; run `cargo run -p xtask -- bluedb-gates`"
                );
                bad = true;
            }
            // The FRESHNESS question, not the integrity one. `body` above is
            // the hashed region — banner + stamps + body — because the sha now
            // covers the stamps. Comparing THAT against a fresh render would
            // diff on `ran:`, which every regeneration legitimately rewrites, so
            // this comparison takes the stamp-free view. The stamps have their
            // own clocks immediately below (`commit:` vs HEAD, full_tier_behind).
            let on_disk_body =
                status::reproducible_body(&on_disk).expect("trailer already located above");
            let fresh_body = status::reproducible_body(fresh).expect("render is well-formed");
            if fresh_body != on_disk_body {
                eprintln!(
                    "bluedb-gates --check: STATUS.md does not match a fresh run — run `cargo run -p xtask -- bluedb-gates`"
                );
                bad = true;
            }
        }
    }

    // Clock 1 — the fast-tier commit.
    if let Some(recorded) = header_field(&on_disk, "commit:") {
        if recorded != head {
            eprintln!(
                "bluedb-gates --check: STATUS.md was generated at {recorded}; HEAD is {head} — stale"
            );
            bad = true;
        }
    }
    // Clock 2 — the full-tier commit.
    match full_tier_behind(root, ledger) {
        None => {
            eprintln!("bluedb-gates --check: the FULL tier has never run — the hardest gates have never executed. Run `cargo run -p xtask -- bluedb-gates --tier=full`");
            bad = true;
        }
        Some(n) if n > 0 => {
            eprintln!("bluedb-gates --check: FULL-tier results are {n} commits behind HEAD — STALE. Run `cargo run -p xtask -- bluedb-gates --tier=full`");
            bad = true;
        }
        Some(_) => {}
    }

    if bad {
        1
    } else {
        println!("bluedb-gates --check: STATUS.md is current and matches a fresh run");
        0
    }
}

fn run_verify_mutations(root: &Path, verbose: bool, only: Option<&str>) -> i32 {
    println!("bluedb-gates --verify-mutations\n");
    let report = match mutations::verify_all(root, verbose, only) {
        Ok(r) => r,
        Err(e) => {
            eprintln!("verify-mutations: {e}");
            return 2;
        }
    };

    if !report.notes.is_empty() {
        println!("\nNotes:");
        for n in &report.notes {
            println!("  {n}");
        }
    }

    let canary_attempted = only.is_none() || only == Some(registry::CANARY_ID);
    println!(
        "\ncanary {}: {}",
        registry::CANARY_ID,
        match (canary_attempted, report.canary_ok) {
            (false, _) => "not attempted (--only selected another gate)",
            (true, true) => "VACUOUS ✔  (the verifier is measuring the worktree)",
            (true, false) => "NOT VACUOUS ✗  (the verifier is not verifying — H3)",
        }
    );

    if !report.failures.is_empty() {
        println!("\nFailures:");
        for f in &report.failures {
            println!("  {f}");
        }
    }

    // The canary is decisive even when it is the only thing that ran.
    if canary_attempted && !report.canary_ok {
        println!("\nVERIFY-MUTATIONS: HARNESS FAIL — the canary did not report VACUOUS");
        return 1;
    }
    if !report.failures.is_empty() {
        println!("\nVERIFY-MUTATIONS: FAIL");
        return 1;
    }
    println!("\nVERIFY-MUTATIONS: PASS");
    0
}

/// The child process the mutation runner executes inside the scratch worktree.
///
/// Emits the machine-readable `PROBE` line the runner parses — including the
/// root it resolved, which is H3 mechanism 2: a binary built from the
/// developer's tree reports the developer's tree and is rejected.
fn run_mutation_probe(root: &Path, only: Option<&str>, verbose: bool) -> i32 {
    let Some(id) = only else {
        eprintln!("--mutation-probe requires --only=<gate>");
        return 2;
    };
    let Some(gate) = registry::find(id) else {
        eprintln!("--mutation-probe: no gate `{id}`");
        return 2;
    };

    // The three static checks bind here too — a patch that makes the registry
    // itself inconsistent must show up as RED, not as a silent skip.
    let static_findings = gates_g0::static_checks();

    let snapshot = std::fs::read_to_string(root.join(status::STATUS_PATH)).ok();
    let exec = if gate.mutations.is_empty() {
        Executed {
            state: GateState::Unproven,
            secs: None,
            detail: "declares no mutation".into(),
            findings: vec![],
        }
    } else {
        run_one(root, gate, Tier::Full, verbose, snapshot)
    };

    let state = if !static_findings.is_empty() && exec.state == GateState::Pass {
        GateState::Fail
    } else {
        exec.state
    };

    println!(
        "PROBE gate={} state={} root={}",
        gate.id,
        state.label().replace(' ', "_"),
        root.display()
    );
    for f in &static_findings {
        println!("  {f}");
    }
    if !exec.detail.is_empty() {
        println!("  {}", exec.detail);
    }
    for f in &exec.findings {
        println!("  {f}");
    }

    if state == GateState::Pass {
        0
    } else {
        1
    }
}

fn bless_baselines(root: &Path) -> i32 {
    let p = root.join("docs/bluedb/baselines.json");
    if p.exists() {
        println!("bluedb-gates --bless: {} exists; no gate consumes it yet, so there is nothing to re-derive.", p.display());
    } else {
        println!(
            "bluedb-gates --bless: no gate declares a baseline artefact yet. Per the U2 ruling the throughput floors are DERIVED FROM `feat/bluedb` and committed as `docs/bluedb/baselines.json` — never seeded from whatever v2 happens to ship. Nothing to bless at P0."
        );
    }
    0
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

fn write_status(root: &Path, rendered: &str) -> std::io::Result<()> {
    let p = root.join(status::STATUS_PATH);
    if let Some(parent) = p.parent() {
        std::fs::create_dir_all(parent)?;
    }
    std::fs::write(p, rendered)
}

/// `None` = the full tier has never run; `Some(n)` = HEAD is n commits ahead.
fn full_tier_behind(root: &Path, ledger: &state::GateState) -> Option<usize> {
    let ft = ledger.full_tier.as_ref()?;
    let out = std::process::Command::new("git")
        .args(["rev-list", "--count", &format!("{}..HEAD", ft.commit)])
        .current_dir(root)
        .output()
        .ok()?;
    if !out.status.success() {
        // An unresolvable recorded commit is maximally stale, not fresh.
        return Some(usize::MAX);
    }
    String::from_utf8_lossy(&out.stdout).trim().parse().ok()
}

fn header_field(text: &str, key: &str) -> Option<String> {
    for line in text.lines().take(4) {
        if let Some(pos) = line.find(key) {
            return line[pos + key.len()..]
                .split_whitespace()
                .next()
                .map(|s| s.to_string());
        }
    }
    None
}

fn now_utc() -> String {
    std::process::Command::new("date")
        .args(["-u", "+%Y-%m-%dT%H:%M:%SZ"])
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_else(|| "unknown".into())
}

fn print_rollup(rows: &[Row], tier: Tier, partial: bool) {
    println!(
        "bluedb-gates — RULE ZERO executable state (tier: {}{})\n",
        tier.label(),
        if partial { ", partial run" } else { "" }
    );

    let verdicts = goal_verdicts(rows);
    println!("{:<44} {}", "Goal", "Verdict");
    println!("{}", "-".repeat(60));
    for (goal, v) in &verdicts {
        let label = match goal {
            0 => "0 — cross-cutting (RULE ZERO harness)",
            1 => "1 — session-bounded Model state sync",
            2 => "2 — unified store, real SERIALIZABLE",
            3 => "3 — easy + simple",
            4 => "4 — notify clients of changesets",
            5 => "5 — console admin access (read+write)",
            _ => "?",
        };
        println!("{label:<44} {}", v.label());
    }

    println!("\n{:<7} {:<9} {:<6} {:<8} {}", "Gate", "Verdict", "Tier", "Time", "Detail");
    println!("{}", "-".repeat(110));
    for r in rows {
        let time = r
            .secs
            .map(|s| format!("{s:.1}s"))
            .unwrap_or_else(|| "—".into());
        println!(
            "{:<7} {:<9} {:<6} {:<8} {}",
            r.id,
            r.state.label(),
            r.tier.label(),
            time,
            truncate(&r.detail, 70)
        );
    }

    let bad: Vec<&Row> = rows.iter().filter(|r| r.state == GateState::Fail).collect();
    if !bad.is_empty() {
        println!("\nFailures");
        println!("{}", "-".repeat(110));
        for r in bad {
            println!("  {} — {}", r.id, r.title);
            for f in &r.findings {
                println!("      {f}");
            }
        }
    }

    let any_fail = rows.iter().any(|r| r.state == GateState::Fail);
    let any_unknown = verdicts.values().any(|v| *v == GoalVerdict::Unknown);
    println!();
    if any_fail {
        println!("BLUEDB GATES: FAIL");
    } else if any_unknown {
        println!("BLUEDB GATES: UNKNOWN — at least one goal has a NOT RUN or unrevalidated gate");
    } else {
        println!("BLUEDB GATES: PASS");
    }
}

fn truncate(s: &str, n: usize) -> String {
    if s.chars().count() <= n {
        s.to_string()
    } else {
        format!("{}…", s.chars().take(n - 1).collect::<String>())
    }
}

fn render_json(rows: &[Row]) -> String {
    let mut out = String::from("{\n  \"goals\": {\n");
    let verdicts = goal_verdicts(rows);
    let n = verdicts.len();
    for (i, (g, v)) in verdicts.iter().enumerate() {
        out.push_str(&format!(
            "    \"{g}\": \"{}\"{}\n",
            v.label(),
            if i + 1 == n { "" } else { "," }
        ));
    }
    out.push_str("  },\n  \"gates\": [\n");
    for (i, r) in rows.iter().enumerate() {
        out.push_str(&format!(
            "    {{ \"id\": \"{}\", \"goal\": {}, \"tier\": \"{}\", \"state\": \"{}\", \"proof\": \"{}\", \"detail\": \"{}\" }}{}\n",
            r.id,
            r.goal,
            r.tier.label(),
            r.state.label(),
            json_escape(&r.proof),
            json_escape(&r.detail),
            if i + 1 == rows.len() { "" } else { "," }
        ));
    }
    out.push_str("  ]\n}\n");
    out
}

fn json_escape(s: &str) -> String {
    s.replace('\\', "\\\\").replace('"', "\\\"").replace('\n', " ")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row(id: &'static str, goal: u8, state: GateState, proof_unknown: bool) -> Row {
        Row {
            id,
            goal,
            title: "t",
            tier: Tier::Fast,
            state,
            secs: None,
            detail: String::new(),
            findings: vec![],
            proof: "—".into(),
            proof_unknown,
        }
    }

    #[test]
    fn a_skipped_tier_renders_unknown_never_pass() {
        let rows = vec![
            row("G2.1", 2, GateState::Pass, false),
            row("G2.3", 2, GateState::NotRun, false),
        ];
        assert_eq!(goal_verdicts(&rows)[&2], GoalVerdict::Unknown);
    }

    #[test]
    fn json_is_well_formed_enough_to_grep() {
        let rows = vec![row("G0.1", 0, GateState::Pass, false)];
        let j = render_json(&rows);
        assert!(j.contains("\"id\": \"G0.1\""));
        assert!(j.contains("\"state\": \"PASS\""));
    }

    #[test]
    fn header_field_reads_the_recorded_commit() {
        let text = "<!-- GENERATED -->\n<!-- commit:      abc12345  ran: x  host: y  tier: fast -->\n";
        assert_eq!(header_field(text, "commit:").as_deref(), Some("abc12345"));
    }
}
