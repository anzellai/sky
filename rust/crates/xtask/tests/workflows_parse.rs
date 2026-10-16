//! Every GitHub workflow must be parseable YAML.
//!
//! This exists because it was not checked, and the consequence was total: a
//! step name containing `: ` —
//!
//! ```yaml
//! - name: Gate — lsp (Neovim editor-parity: 17 symbol-class + 32 corpus cases)
//! ```
//!
//! is invalid inside an unquoted plain scalar. GitHub could not parse
//! `rust-ci.yml`, so the run died in 0 s with "workflow file issue" and EVERY
//! job — corpus, macOS determinism, lsp-fuzz, postgres, the budget assert —
//! was skipped. A compiler change reached `main` with zero CI verification, and
//! the failure looked like a red X rather than like nothing having run.
//!
//! `xtask ci-scan` could not catch it: it reads workflows LINE BY LINE to
//! extract gate names and never parses the document. So the cycle whose premise
//! is "a gate that cannot fail is worse than no gate" ended with a CI
//! definition that could not RUN, guarded by nothing.
//!
//! Parsing is the whole assertion. A workflow that parses can still be wrong;
//! a workflow that does not parse cannot check anything at all.

use std::path::PathBuf;

fn workflows() -> Vec<PathBuf> {
    let dir = PathBuf::from(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../.github/workflows"
    ));
    let mut out: Vec<PathBuf> = std::fs::read_dir(&dir)
        .unwrap_or_else(|e| panic!("cannot read {}: {e}", dir.display()))
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| matches!(p.extension().and_then(|x| x.to_str()), Some("yml" | "yaml")))
        .collect();
    out.sort();
    assert!(
        !out.is_empty(),
        "no workflows found — the glob is wrong, not the repo"
    );
    out
}

#[test]
fn every_workflow_is_parseable_yaml() {
    let mut bad = Vec::new();
    for path in workflows() {
        let text = std::fs::read_to_string(&path).expect("read workflow");
        if let Err(e) = serde_yaml::from_str::<serde_yaml::Value>(&text) {
            bad.push(format!("{}: {e}", path.display()));
        }
    }
    assert!(
        bad.is_empty(),
        "workflow(s) are not valid YAML — GitHub will refuse the whole file and \
         run NOTHING:\n  {}\nA `:` followed by a space inside an unquoted step \
         name is the usual cause; wrap the value in single quotes.",
        bad.join("\n  ")
    );
}

/// A parseable workflow must still have the shape GitHub requires, or it parses
/// and then does nothing useful.
#[test]
fn every_workflow_declares_jobs_with_steps() {
    for path in workflows() {
        let text = std::fs::read_to_string(&path).expect("read workflow");
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parsed above");
        let jobs = doc
            .get("jobs")
            .unwrap_or_else(|| panic!("{}: no `jobs`", path.display()));
        let map = jobs
            .as_mapping()
            .unwrap_or_else(|| panic!("{}: `jobs` is not a mapping", path.display()));
        assert!(!map.is_empty(), "{}: `jobs` is empty", path.display());
        for (name, job) in map {
            let name = name.as_str().unwrap_or("<non-string>");
            // A job either runs steps or delegates to a reusable workflow.
            let has_steps = job
                .get("steps")
                .and_then(|s| s.as_sequence())
                .is_some_and(|s| !s.is_empty());
            let has_uses = job.get("uses").is_some();
            assert!(
                has_steps || has_uses,
                "{}: job `{name}` has neither `steps` nor `uses`",
                path.display()
            );
        }
    }
}

/// The fan-in must actually fan in.
///
/// `ci-green` asserts every job in its `needs` succeeded, and is the check
/// INTENDED to be the single required one. A job that is NOT in that list still
/// runs and still shows a red X on the run, but `ci-green` goes green without
/// it — so once promoted, that job stops blocking merges.
///
/// STATE OF THE WORLD, verified 2026-08-13 against
/// `GET /branches/main/protection`: `required_status_checks.contexts` is
/// `["ci-green"]`. So the property IS load-bearing — a job missing from the
/// `needs` list genuinely stops blocking merges.
///
/// This docstring has now been wrong in BOTH directions, which is worth leaving
/// on the record. It first claimed required checks were enabled when they were
/// not (a Judge caught it). It was corrected to "no required status checks at
/// all" — true at that moment — and then `ci-green` was promoted an hour later
/// and the docstring was not updated, so a second Judge caught the same
/// sentence being false with the sign flipped.
///
/// The lesson is not "write it more carefully". It is that a fact about repo
/// SETTINGS cannot be verified from inside this tree, so any statement about it
/// here is a snapshot with a date attached, not an invariant. If it matters
/// enough to assert, query the API; otherwise say when it was last checked and
/// let the reader re-check.
///
/// Weakenings worth knowing, same query: `strict: false` (no up-to-date
/// requirement before merge), `enforce_admins: false`,
/// `required_approving_review_count: 0`.
///
/// So the `needs` list is load-bearing, and it is hand-maintained. Adding a job
/// and forgetting the one-line `needs` entry produces a gate that reports and
/// enforces nothing, which is indistinguishable from the failure this whole
/// cycle was opened to remove.
///
/// This is the general form of a hole found on 2026-08-12: the harness `T2`
/// tier — 383 behavioural assertions including the `Dict` key×operation
/// crossing built after #174 escaped — was in no workflow at all. Wiring it in
/// fixed that instance; this test is what stops the next one, one layer up.
#[test]
fn ci_green_needs_every_other_job_in_its_workflow() {
    let path = PathBuf::from(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../.github/workflows/rust-ci.yml"
    ));
    let text = std::fs::read_to_string(&path).expect("read rust-ci.yml");
    let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("rust-ci.yml parses");
    let jobs = doc
        .get("jobs")
        .and_then(|j| j.as_mapping())
        .expect("`jobs` mapping");

    let all: Vec<String> = jobs
        .keys()
        .filter_map(|k| k.as_str())
        .filter(|n| *n != "ci-green")
        .map(str::to_string)
        .collect();
    assert!(
        all.len() > 5,
        "only {} job(s) found besides ci-green — the parse is wrong, and a test \
         that inspects nothing passes silently",
        all.len()
    );

    let needs = jobs
        .get(serde_yaml::Value::from("ci-green"))
        .and_then(|g| g.get("needs"))
        .expect("ci-green declares `needs`");
    let declared: Vec<String> = match needs {
        serde_yaml::Value::Sequence(s) => s
            .iter()
            .filter_map(|v| v.as_str())
            .map(str::to_string)
            .collect(),
        serde_yaml::Value::String(s) => vec![s.clone()],
        other => panic!("ci-green `needs` is neither a list nor a string: {other:?}"),
    };

    let missing: Vec<&String> = all.iter().filter(|j| !declared.contains(j)).collect();
    assert!(
        missing.is_empty(),
        "job(s) in rust-ci.yml are NOT in `ci-green.needs`, so they do not block a \
         merge — the required check can go green while they are red:\n  {}\n\
         Add each to the `needs:` list.",
        missing
            .iter()
            .map(|s| s.as_str())
            .collect::<Vec<_>>()
            .join("\n  ")
    );

    // The inverse: a `needs` entry naming a job that no longer exists makes the
    // whole fan-in job fail to schedule, which branch protection sees as a
    // missing check rather than a failure.
    let phantom: Vec<&String> = declared.iter().filter(|d| !all.contains(d)).collect();
    assert!(
        phantom.is_empty(),
        "ci-green `needs` names job(s) that do not exist in rust-ci.yml:\n  {}",
        phantom
            .iter()
            .map(|s| s.as_str())
            .collect::<Vec<_>>()
            .join("\n  ")
    );
}

/// Every harness TIER that has gates must be invoked by something.
///
/// This is the general form of a defect found three times in one cycle. Gate
/// selection is exact tier equality (`harness/mod.rs`: `g.tier ==
/// o.tier.unwrap_or(Tier::T1)`), so `--tier t1` runs T1 and nothing else. A gate
/// registered at any other tier is therefore invisible unless some workflow or
/// script names that tier explicitly.
///
/// What that cost, in order of discovery:
///   * **T2** — 383 behavioural assertions, including the `Dict` key ×
///     access-shape crossing built after #174's `Dict.foldl` panic reached a
///     release. Executed by nothing.
///   * **T3** — `apps-ledger-postgres`, `apps-dispatch-postgres`, `apps-fleet`.
///     Executed by nothing.
///   * **T4** — `apps-ffi-scale`, the pre-release FFI-scale benchmark whose
///     entire purpose is to run at release. Executed by nothing.
///
/// Each was registered, budgeted, documented, and dead. The registry even
/// records falsifying mutations proving some of them CAN fail — proofs against
/// gates nothing invoked.
///
/// A tier nobody runs is indistinguishable from a tier that does not exist, so
/// this asserts the invocation exists rather than trusting that someone
/// remembered. It deliberately checks BOTH workflows and scripts: a tier run
/// only by `preflight-tag.sh` is weaker than one in CI, but it is not dead, and
/// this test is about deadness.
#[test]
fn every_harness_tier_with_gates_is_invoked_somewhere() {
    let repo = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));

    // Which tiers the registry actually assigns to gates.
    let registry = std::fs::read_to_string(repo.join("rust/crates/xtask/src/harness/registry.rs"))
        .expect("read registry.rs");
    let mut needed: Vec<&str> = Vec::new();
    for (marker, flag) in [
        ("Tier::T1", "t1"),
        ("Tier::T2", "t2"),
        ("Tier::T3", "t3"),
        ("Tier::T4", "t4"),
    ] {
        if registry.contains(marker) {
            needed.push(flag);
        }
    }
    assert!(
        needed.len() >= 3,
        "found only {} tier(s) in registry.rs — the scan is wrong, and a test \
         that inspects nothing passes silently",
        needed.len()
    );

    // Everything that could invoke one.
    // EXECUTABLE content only.
    //
    // This scanned raw file text until a Judge defeated it: the explanatory
    // comments THIS VERY CYCLE added to `nightly-sweep.yml` —
    //
    //   # that reviewed that fix: `harness --tier t3` was invoked by NOTHING.
    //   # `release.yml`'s `--tier t1` and (as of this cycle) nightly's `--tier t2`.
    //
    // contain the exact strings the scan looked for. Deleting BOTH real
    // invocations left the test green. A gate satisfied by prose ABOUT the gate
    // is the purest form of the vacuity this cycle exists to remove, and I wrote
    // the prose that satisfied it.
    //
    // So: workflows are PARSED and only `jobs.*.steps[].run` bodies count;
    // scripts have comment lines stripped. A comment can no longer vouch for an
    // invocation that is not there.
    let mut haystack = String::new();
    for path in workflows() {
        let Ok(text) = std::fs::read_to_string(&path) else {
            continue;
        };
        let Ok(doc) = serde_yaml::from_str::<serde_yaml::Value>(&text) else {
            continue;
        };
        let Some(jobs) = doc.get("jobs").and_then(|j| j.as_mapping()) else {
            continue;
        };
        for (_, job) in jobs {
            let Some(steps) = job.get("steps").and_then(|s| s.as_sequence()) else {
                continue;
            };
            for step in steps {
                if let Some(run) = step.get("run").and_then(|r| r.as_str()) {
                    haystack.push_str(run);
                    haystack.push('\n');
                }
            }
        }
    }
    let scripts = repo.join("scripts");
    if let Ok(rd) = std::fs::read_dir(&scripts) {
        for e in rd.filter_map(|e| e.ok()) {
            let p = e.path();
            if !p.is_file() {
                continue;
            }
            let Ok(t) = std::fs::read_to_string(&p) else {
                continue;
            };
            for line in t.lines() {
                if !line.trim_start().starts_with('#') {
                    haystack.push_str(line);
                    haystack.push('\n');
                }
            }
        }
    }
    assert!(
        haystack.contains("--tier"),
        "no `--tier` invocation found in any workflow `run:` step or script — \
         the scan is broken, not the repo"
    );

    let dead: Vec<&str> = needed
        .iter()
        .copied()
        .filter(|t| !haystack.contains(&format!("--tier {t}")))
        .collect();
    assert!(
        dead.is_empty(),
        "harness tier(s) have registered gates but are invoked by NO workflow \
         and NO script: {}\n\n\
         Gate selection is exact tier equality, so those gates run nowhere — \
         registered, budgeted, and dead. Add a `harness --tier <t>` invocation \
         to a workflow (or, for a pre-release tier, to release.yml), or move the \
         gates to a tier that runs.",
        dead.join(", ")
    );
}

/// Every workflow that gates a COMMIT to `main` must be one the required check
/// actually covers.
///
/// `ci_green_needs_every_other_job_in_its_workflow` inspects `rust-ci.yml` and
/// nothing else, and a Judge defeated it exactly there: drop in a second
/// push-triggered workflow with a failing job and all three tests stay green,
/// because `ci-green` — the single required status check — knows nothing about
/// other files. The job goes red on the run and the merge proceeds.
///
/// So the fan-in is only as good as the set of workflows it can see. A workflow
/// that runs per-commit on `main` is either:
///   * `rust-ci.yml`, whose jobs `ci-green` fans in and which the sibling test
///     keeps complete; or
///   * on the list below, WITH a reason — meaning someone decided out loud that
///     its failure should not block a merge.
///
/// Anything else is a gate whose verdict nobody is required to honour, which is
/// the same shape as a gate that does not run.
///
/// Workflows triggered only by tags, schedules, or manual dispatch are out of
/// scope: they do not gate a commit, and their coverage is asserted by
/// `every_harness_tier_with_gates_is_invoked_somewhere` instead.
const NON_BLOCKING_PER_COMMIT_WORKFLOWS: &[(&str, &str)] = &[(
    "docs-site.yml",
    "publishes the GitHub Pages docs site. A publish failure is a broken \
     deploy, not a broken compiler, and holding merges on it would couple the \
     source of truth to a hosting side effect.",
)];

#[test]
fn every_per_commit_workflow_is_covered_by_the_required_check_or_declared() {
    let mut offenders = Vec::new();
    let mut checked = 0usize;

    for path in workflows() {
        let name = path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or_default()
            .to_string();
        let text = std::fs::read_to_string(&path).expect("read workflow");
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parses");

        // `on:` — note YAML parses a bare `on` key as the BOOLEAN true, which is
        // why this looks up both spellings. Getting that wrong makes the test
        // silently inspect nothing.
        let on = doc
            .get("on")
            .or_else(|| doc.get(serde_yaml::Value::Bool(true)));
        let Some(on) = on else { continue };

        let gates_a_commit = ["push", "pull_request"].iter().any(|trigger| {
            let Some(t) = on.get(*trigger) else {
                return false;
            };
            // A trigger gates a commit UNLESS it is restricted to tags only.
            //
            // The previous form was `has_branches || (!has_tags &&
            // !t.is_mapping())`, and a Judge defeated it: `push: { paths: ['**'] }`
            // is a mapping, has no `tags`, has no `branches` — so it was
            // classified as NOT gating a commit, while GitHub runs it on every
            // push to main. `branches-ignore:` defeated it the same way. The
            // probe was a workflow whose only job is `run: exit 1`, and all
            // three tests stayed green.
            //
            // Inverted to fail safe: only an explicitly TAG-ONLY trigger is
            // exempt. Anything else — a bare `push:`, `paths:`,
            // `branches-ignore:`, a shape not thought of yet — counts as
            // commit-gating and must be covered or declared. A new trigger form
            // now lands on the strict side by default.
            let is_tag_only = t.is_mapping()
                && t.get("tags").is_some()
                && t.get("branches").is_none()
                && t.get("branches-ignore").is_none()
                && t.get("paths").is_none()
                && t.get("paths-ignore").is_none();
            !is_tag_only
        });
        if !gates_a_commit {
            continue;
        }
        checked += 1;

        let covered = name == "rust-ci.yml"
            || NON_BLOCKING_PER_COMMIT_WORKFLOWS
                .iter()
                .any(|(n, _)| *n == name);
        if !covered {
            offenders.push(name);
        }
    }

    assert!(
        checked >= 2,
        "only {checked} per-commit workflow(s) found — the trigger parse is \
         wrong (a bare `on:` key parses as the boolean true in YAML), and a test \
         that inspects nothing passes silently"
    );
    assert!(
        offenders.is_empty(),
        "workflow(s) run on every commit to `main` but are NOT covered by the \
         `ci-green` required check:\n  {}\n\n\
         `ci-green` fans in `rust-ci.yml` only, so these can go red while the \
         required check goes green and the merge proceeds. Either move the jobs \
         into rust-ci.yml (where the fan-in keeps them), or add the workflow to \
         NON_BLOCKING_PER_COMMIT_WORKFLOWS with a written reason — an explicit \
         decision that its failure should not block a merge.",
        offenders.join("\n  ")
    );
}

/// The allowlist must carry reasons, not just names. An entry with an empty
/// reason is a silent exemption, which is the thing being prevented.
#[test]
fn every_non_blocking_declaration_states_why() {
    for (name, reason) in NON_BLOCKING_PER_COMMIT_WORKFLOWS {
        assert!(
            reason.len() > 40,
            "`{name}` is exempted from the required check with no real reason \
             ({} chars). Write why its failure must not block a merge.",
            reason.len()
        );
        assert!(
            workflows()
                .iter()
                .any(|p| p.file_name().and_then(|n| n.to_str()) == Some(*name)),
            "`{name}` is exempted but no such workflow exists — a stale \
             exemption silently widens the hole it was carved for"
        );
    }
}

// ---------------------------------------------------------------------------
// Contexts a job-level key is not allowed to name
// ---------------------------------------------------------------------------

/// Contexts that are available in NO job-level key.
///
/// GitHub's context-availability table varies per key — `matrix` is legal in a
/// job `env:` and not in `runs-on:`, `secrets` is legal in `env:` and not in
/// `if:` — so this list is deliberately the intersection: four contexts that no
/// job-level key may ever name, which is a rule with no false positives.
///
/// `runner` is the one that bit. `nightly-sweep.yml` carried
///
/// ```yaml
///     env:
///       OUT: ${{ runner.temp }}/bundles
/// ```
///
/// on a job, and the penalty is not a warning about one variable: GitHub
/// refuses to parse the FILE. The run is created, has zero jobs, finishes in
/// 0 s and is attributed to whatever push arrived — so the nightly sweep, the
/// browser tier and the real-bundle licence gate all stopped running while the
/// red looked like a nightly problem on a branch nobody reads.
///
/// That is the same outage this file's header describes, reached through a
/// different door: `every_workflow_is_parseable_yaml` passes, because the YAML
/// is fine and it is GitHub's expression checker that refuses it.
const NO_JOB_LEVEL_KEY_MAY_USE: [&str; 4] = ["runner", "steps", "job", "env"];

/// Job-level keys evaluated BEFORE the job has a runner, a step or an
/// environment — so an expression in one of them cannot name those.
const JOB_LEVEL_KEYS: [&str; 5] = [
    "env",
    "if",
    "runs-on",
    "timeout-minutes",
    "continue-on-error",
];

/// Every `${{ … }}` span in a string, inner text only.
fn expressions(text: &str) -> Vec<&str> {
    let mut out = Vec::new();
    let mut rest = text;
    while let Some(open) = rest.find("${{") {
        rest = &rest[open + 3..];
        match rest.find("}}") {
            Some(close) => {
                out.push(&rest[..close]);
                rest = &rest[close + 2..];
            }
            // Unterminated — GitHub would reject it too, but that is the
            // parser's complaint to make, not this test's.
            None => break,
        }
    }
    out
}

/// The context names an expression reads: an identifier immediately followed
/// by `.`. `runner.temp` yields `runner`; `fromJSON('["a"]')` yields nothing.
fn contexts_used(expr: &str) -> Vec<String> {
    let bytes: Vec<char> = expr.chars().collect();
    let mut out = Vec::new();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i].is_ascii_alphabetic() || bytes[i] == '_' {
            let start = i;
            while i < bytes.len()
                && (bytes[i].is_ascii_alphanumeric() || bytes[i] == '_' || bytes[i] == '-')
            {
                i += 1;
            }
            if i < bytes.len() && bytes[i] == '.' {
                out.push(bytes[start..i].iter().collect());
            }
        } else {
            i += 1;
        }
    }
    out
}

/// Every scalar string reachable from a YAML value, however nested.
fn scalars(v: &serde_yaml::Value, out: &mut Vec<String>) {
    match v {
        serde_yaml::Value::String(s) => out.push(s.clone()),
        serde_yaml::Value::Sequence(xs) => xs.iter().for_each(|x| scalars(x, out)),
        serde_yaml::Value::Mapping(m) => m.iter().for_each(|(k, x)| {
            scalars(k, out);
            scalars(x, out);
        }),
        _ => {}
    }
}

#[test]
fn job_level_keys_name_only_contexts_that_exist_yet() {
    let mut bad = Vec::new();
    for path in workflows() {
        let text = std::fs::read_to_string(&path).expect("read workflow");
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parsed above");
        let Some(jobs) = doc.get("jobs").and_then(|j| j.as_mapping()) else {
            continue;
        };
        for (job_name, job) in jobs {
            let job_name = job_name.as_str().unwrap_or("<non-string>");
            for key in JOB_LEVEL_KEYS {
                let Some(value) = job.get(key) else { continue };
                let mut strings = Vec::new();
                scalars(value, &mut strings);
                for s in &strings {
                    for expr in expressions(s) {
                        for ctx in contexts_used(expr) {
                            if NO_JOB_LEVEL_KEY_MAY_USE.contains(&ctx.as_str()) {
                                bad.push(format!(
                                    "{}: job `{job_name}` key `{key}` uses `{ctx}.` in `${{{{{expr}}}}}`",
                                    path.display()
                                ));
                            }
                        }
                    }
                }
            }
        }
    }
    assert!(
        bad.is_empty(),
        "a job-level key names a context that does not exist at job level. \
         GitHub does not warn: it REFUSES THE WHOLE FILE, and the run then has \
         zero jobs, zero logs and a 0 s red that reads like a flake:\n  {}\n\
         Set the value from a step instead — `echo \"OUT=$RUNNER_TEMP/x\" >> \
         \"$GITHUB_ENV\"` — or move the expression to the step that uses it, \
         where `runner`/`steps`/`env` are available.",
        bad.join("\n  ")
    );
}

/// The gate must be able to go red. A test that only ever sees a clean tree
/// proves nothing about what it would do with a dirty one.
#[test]
fn the_job_level_context_check_catches_the_shape_that_broke_nightly_sweep() {
    let doc: serde_yaml::Value = serde_yaml::from_str(
        "jobs:\n  b:\n    runs-on: ubuntu-latest\n    env:\n      OUT: ${{ runner.temp }}/bundles\n    steps:\n      - run: true\n",
    )
    .expect("fixture parses");
    let env = doc
        .get("jobs")
        .and_then(|j| j.get("b"))
        .and_then(|b| b.get("env"))
        .expect("fixture has a job env");
    let mut strings = Vec::new();
    scalars(env, &mut strings);
    let found: Vec<String> = strings
        .iter()
        .flat_map(|s| expressions(s))
        .flat_map(contexts_used)
        .filter(|c| NO_JOB_LEVEL_KEY_MAY_USE.contains(&c.as_str()))
        .collect();
    assert_eq!(
        found,
        vec!["runner".to_string()],
        "the check no longer sees the original defect"
    );

    // …and does not fire on the expressions the workflows legitimately use.
    for ok in [
        "${{ github.workspace }}/.gocache",
        "${{ needs.setup.outputs.sha }}",
        "${{ github.event_name != 'workflow_dispatch' || contains(fromJSON('[\"both\"]'), inputs.only) }}",
        "${{ matrix.os }}",
        "${{ secrets.GITHUB_TOKEN }}",
    ] {
        let hits: Vec<String> = expressions(ok)
            .into_iter()
            .flat_map(contexts_used)
            .filter(|c| NO_JOB_LEVEL_KEY_MAY_USE.contains(&c.as_str()))
            .collect();
        assert!(hits.is_empty(), "false positive on `{ok}`: {hits:?}");
    }
}

/// The census + ratchet gates must run on a PR, not only at release.
///
/// # The defect
///
/// `config-surface` and `config-matrix` are both `Tier::T1`, and the repo's
/// only `harness --tier t1` invocation lived in `release.yml`; `denominators
/// --check` and `coverage-ledger --check` were likewise release-only steps.
/// Gate selection is exact tier equality, so nothing on a pull request could
/// reach any of them.
///
/// The consequence was live on this branch: `xtask config-surface --check` was
/// RED on a clean tree at HEAD — `STALE — summary.documented_names: 88 -> 89` —
/// because `SKY_HTTP_CLIENT_TIMEOUT` was added one commit AFTER the census was
/// regenerated. The series left its own gate red at merge and nothing noticed,
/// because nothing ran it until a tag was cut.
///
/// # What this asserts
///
/// Each invocation below appears in the `run:` body of a `rust-ci.yml` job that
/// `ci-green` fans in — the only place a step both runs on a PR AND blocks a
/// merge. Parsed, never grepped as raw text: a comment mentioning an invocation
/// must not vouch for one, which is exactly how the sibling tier-invocation
/// test was defeated once already.
const REQUIRED_ON_PR: &[(&str, &str)] = &[
    (
        "config-surface --check",
        "the sky.toml + env census. Drifts on any commit that adds a documented \
         name or a reader, which is to say on ordinary work",
    ),
    (
        "denominators --check",
        "how much surface exists. A DECREASE is a coverage claim shrinking",
    ),
    (
        "coverage-ledger --check",
        "how strongly each surface is covered. A surface getting weaker fails here",
    ),
    (
        "--only config-matrix",
        "every covered setting's effective value, observed from running binaries",
    ),
];

#[test]
fn the_census_and_ratchet_gates_run_on_a_pull_request() {
    let path = PathBuf::from(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../.github/workflows/rust-ci.yml"
    ));
    let text = std::fs::read_to_string(&path).expect("read rust-ci.yml");
    let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("rust-ci.yml parses");
    let jobs = doc
        .get("jobs")
        .and_then(|j| j.as_mapping())
        .expect("`jobs` mapping");

    // Only jobs the required check fans in count: a step in a job `ci-green`
    // does not need can go red while the merge proceeds.
    let fanned: Vec<String> = jobs
        .get(serde_yaml::Value::from("ci-green"))
        .and_then(|g| g.get("needs"))
        .and_then(|n| n.as_sequence())
        .map(|s| {
            s.iter()
                .filter_map(|v| v.as_str())
                .map(str::to_string)
                .collect()
        })
        .expect("ci-green declares a `needs` list");
    assert!(
        fanned.len() > 5,
        "ci-green fans in only {} job(s) — the parse is wrong",
        fanned.len()
    );

    let mut haystack = String::new();
    for (name, job) in jobs {
        let Some(name) = name.as_str() else { continue };
        if !fanned.contains(&name.to_string()) {
            continue;
        }
        let Some(steps) = job.get("steps").and_then(|s| s.as_sequence()) else {
            continue;
        };
        for step in steps {
            if let Some(run) = step.get("run").and_then(|r| r.as_str()) {
                haystack.push_str(run);
                haystack.push('\n');
            }
        }
    }
    assert!(
        haystack.contains("xtask"),
        "no `xtask` invocation found in any fanned-in job's `run:` body — the \
         scan is broken, not the repo"
    );

    let missing: Vec<String> = REQUIRED_ON_PR
        .iter()
        .filter(|(inv, _)| !haystack.contains(inv))
        .map(|(inv, why)| format!("`{inv}` — {why}"))
        .collect();
    assert!(
        missing.is_empty(),
        "gate invocation(s) run only at RELEASE, so drift lands on `main` \
         unnoticed and is discovered when a tag is cut:\n  {}\n\n\
         Add each to a `rust-ci.yml` job that `ci-green` needs.",
        missing.join("\n  ")
    );
}

/// The release gate must run the FULL tier suite — every tier, nothing deferred
/// to the nightly. This makes CLAUDE.md §0.2.1 (INVIOLABLE) structural.
///
/// # Why this exists
///
/// The per-commit `ci-green` fan-in runs a deliberately LIGHT tier so commits
/// stay fast, and the heaviest gates — the T2 `behaviour-corpus` (which `go
/// build`s AND RUNS every combinatorial fixture), the T3 Postgres app tier, the
/// falsifier proofs, and the full `example-sweep` (build AND run every example,
/// not just `sky check`) — ran ONLY in the nightly. That split is correct for a
/// commit and WRONG for a release: a release is the point where *everything we
/// know how to test must be green* is the gate.
///
/// The lesson (2026-08-20): a `record_update` typed-emit codegen regression
/// shipped in v0.21.0. It type-checked, all examples built, both downstream apps
/// deployed — and the nightly T2 behaviour-corpus caught it the MORNING AFTER
/// the release, one tier too late, after users could already `sky upgrade`. A
/// gate we own found a defect a gate we own should have blocked at the tag.
///
/// # What this asserts
///
/// The release gate is fanned out into several concurrent jobs (`gate-core`,
/// `gate-t2`, `gate-app`, `gate-falsifiers`, `gate-sweep`) that `release:
/// needs:` all list, so publication is blocked until every one is green. The
/// UNION of their `run:` bodies must invoke, at minimum: the T2 tier, the T3
/// tier, the falsifier verification, and a full `example-sweep.sh` that is NOT
/// `--build-only` (it must RUN each example, not just compile it). T1 and T4
/// already had explicit tests / were present; T2/T3/falsifiers/sweep are the
/// tiers that were nightly-only and are the point of §0.2.1.
///
/// It scans every job whose name starts with `gate` (the release gate family),
/// so splitting a tier into its own sibling job does not create a gap — the
/// invocation only has to live in ONE of them.
///
/// Parsed, never grepped as raw text — a comment mentioning `--tier t2` must not
/// vouch for an invocation that is not there, which is exactly how the sibling
/// tier-invocation test was defeated once already.
#[test]
fn the_release_gate_runs_the_full_tier_suite() {
    let path = PathBuf::from(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../.github/workflows/release.yml"
    ));
    let text = std::fs::read_to_string(&path).expect("read release.yml");
    let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("release.yml parses");
    let jobs = doc
        .get("jobs")
        .and_then(|j| j.as_mapping())
        .expect("`jobs` mapping");

    // Every job in the release gate family (`gate-*`). The old single `gate`
    // job was split into these; collecting all of them means an invocation
    // moving between sibling jobs is still counted.
    let gate_jobs: Vec<(&str, &serde_yaml::Value)> = jobs
        .iter()
        .filter_map(|(k, v)| k.as_str().map(|n| (n, v)))
        .filter(|(n, _)| n.starts_with("gate"))
        // A job switched off with `if: false` runs nothing. Its `run:` bodies must
        // not vouch for a tier — that is how the interim "lean release" kept this
        // test green while T2 and the falsifier proofs ran in no release job.
        .filter(|(_, v)| !job_is_disabled(v))
        .collect();
    assert!(
        gate_jobs.len() >= 2,
        "found {} release gate job(s) whose name starts with `gate` — the split \
         collapsed or the parse is wrong",
        gate_jobs.len()
    );

    // EXECUTABLE content only — `run:` bodies across every gate job, never step
    // names or comments.
    let mut haystack = String::new();
    for (_name, job) in &gate_jobs {
        let Some(steps) = job.get("steps").and_then(|s| s.as_sequence()) else {
            continue;
        };
        for step in steps {
            if let Some(run) = step.get("run").and_then(|r| r.as_str()) {
                haystack.push_str(run);
                haystack.push('\n');
            }
        }
    }
    assert!(
        haystack.contains("xtask"),
        "no `xtask` invocation found in any `gate*` job step `run:` body — the \
         parse is broken, not the repo"
    );

    // Each required invocation, with why it is load-bearing at a release.
    let required: &[(&str, &str)] = &[
        (
            "--tier t2",
            "the T2 behaviour-corpus — `go build`s AND RUNS every combinatorial \
             fixture. It caught the v0.21.0 `record_update` regression a tier too \
             late because it was nightly-only",
        ),
        (
            "--tier t3",
            "the T3 Layer-2 app tier (the ONLY Postgres app coverage). Needs the \
             `services: postgres` container declared on the gate job",
        ),
        (
            "--verify-falsifiers",
            "proves each T1 gate's declared mutation STILL reddens it — the proofs \
             `--require-proofs` leans on. Ran only in the nightly",
        ),
    ];
    let missing: Vec<String> = required
        .iter()
        .filter(|(inv, _)| !haystack.contains(inv))
        .map(|(inv, why)| format!("`{inv}` — {why}"))
        .collect();

    // The example-sweep must build AND run. A `--build-only` invocation compiles
    // every example but never executes one, so it would NOT have caught the
    // v0.21.0 regression (which type-checked and built); it must be absent.
    let sweep_present = haystack.contains("example-sweep.sh");
    let sweep_is_build_only = haystack.contains("example-sweep.sh --build-only")
        || haystack.contains("example-sweep.sh")
            && haystack
                .lines()
                .filter(|l| l.contains("example-sweep.sh"))
                .any(|l| l.contains("--build-only"));

    let mut problems = missing;
    if !sweep_present {
        problems.push(
            "`example-sweep.sh` — the full clean-slate sweep that builds AND RUNS \
             every example incl. FFI/Std.Db. Ran only in the nightly"
                .to_string(),
        );
    } else if sweep_is_build_only {
        problems.push(
            "`example-sweep.sh` is invoked with `--build-only`, which only \
             compiles — it must RUN each example (drop the flag), or a regression \
             that builds but panics at runtime ships"
                .to_string(),
        );
    }

    assert!(
        problems.is_empty(),
        "release.yml's `gate*` jobs do NOT run the full tier suite. CLAUDE.md \
         §0.2.1 (INVIOLABLE) requires every tier at a release — nothing deferred \
         to the nightly, which is exactly the gap that let the v0.21.0 \
         `record_update` codegen regression ship. Missing:\n  {}\n\n\
         Add each to a `run:` step of one of the `gate-*` jobs (T3 also needs the \
         `services: postgres` container on that job, and every `gate-*` job must \
         be in `release: needs:`). Mirror nightly-sweep.yml's invocations.",
        problems.join("\n  ")
    );
}

/// In the release gate, the step that installs the compiler (`scripts/build.sh`
/// → `sky-out/sky`) MUST run BEFORE the codegen build+run step (`xtask
/// build-run`, whose `live`-shape examples spawn `sky build`).
///
/// The v0.23.0 release publish failed at exactly this point: `build-run --all`
/// ran first, so every live example died with `sky build spawn: No such file or
/// directory (os error 2)` while four platform binaries had already built —
/// publication blocked on step order, not a defect. The fix was a hand-edit to
/// the order; this test locks it, because the other checks in this file are
/// presence-only and both steps WERE present — just in the wrong order.
///
/// The gate is now split across `gate-*` jobs and the compiler install lives in
/// the `.github/actions/gate-setup` COMPOSITE action (a `uses:` step), so this
/// asserts, in whichever gate job runs `build-run`, that a compiler-installing
/// step precedes it — either a `run:` invoking `scripts/build.sh` (belt) or the
/// `gate-setup` composite that runs it (braces). It also asserts the composite
/// actually installs the compiler, so the `uses:` reference is not vacuous.
#[test]
fn release_gate_installs_the_compiler_before_build_run() {
    let path = workflows()
        .into_iter()
        .find(|p| p.file_name().and_then(|n| n.to_str()) == Some("release.yml"))
        .expect("release.yml must exist");
    let text = std::fs::read_to_string(&path).expect("read release.yml");
    let doc: serde_yaml::Value =
        serde_yaml::from_str(&text).expect("release.yml is parseable YAML");
    let jobs = doc
        .get("jobs")
        .and_then(|j| j.as_mapping())
        .expect("release.yml has jobs");

    // Find the gate job that runs `build-run`, and the index of that step.
    let mut found = false;
    for (name, job) in jobs {
        let Some(name) = name.as_str() else { continue };
        if !name.starts_with("gate") {
            continue;
        }
        let Some(steps) = job.get("steps").and_then(|s| s.as_sequence()) else {
            continue;
        };
        let mut install_idx: Option<usize> = None;
        let mut build_run_idx: Option<usize> = None;
        for (i, step) in steps.iter().enumerate() {
            let run = step.get("run").and_then(|r| r.as_str()).unwrap_or("");
            let uses = step.get("uses").and_then(|u| u.as_str()).unwrap_or("");
            // A step installs the compiler if it runs build.sh directly OR uses
            // the gate-setup composite action (which runs build.sh).
            if run.contains("build.sh") || uses.contains("gate-setup") {
                install_idx.get_or_insert(i);
            }
            if run.contains("build-run") {
                build_run_idx.get_or_insert(i);
            }
        }
        let Some(brun) = build_run_idx else { continue };
        found = true;
        let inst = install_idx.unwrap_or_else(|| {
            panic!(
                "release.yml `{name}` runs `build-run` but installs the compiler \
                 nowhere before it (no scripts/build.sh `run:` and no `uses: \
                 ./.github/actions/gate-setup`). build-run's live-shape examples \
                 spawn `sky build`, which needs sky-out/sky to exist."
            )
        });
        assert!(
            inst < brun,
            "release.yml `{name}` runs `build-run` (step {brun}) BEFORE it installs the \
             compiler (step {inst}, scripts/build.sh or the gate-setup composite). \
             build-run's live-shape examples spawn `sky build`, which needs sky-out/sky \
             to exist — the v0.23.0 publish failed with `sky build spawn: No such file or \
             directory` for exactly this reason. Move the compiler install ahead of it."
        );
    }
    assert!(
        found,
        "no release gate job runs `xtask build-run` — the codegen build+run gate \
         is missing from release.yml"
    );

    // The `uses: ./.github/actions/gate-setup` reference is only a compiler
    // install if the composite actually runs build.sh. Assert that, so the
    // braces branch above cannot be satisfied by an empty action.
    let action = PathBuf::from(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../.github/actions/gate-setup/action.yml"
    ));
    let atext = std::fs::read_to_string(&action)
        .expect("release gate jobs use ./.github/actions/gate-setup — its action.yml must exist");
    assert!(
        atext.contains("build.sh"),
        "the gate-setup composite action does not run scripts/build.sh, so a gate \
         job that only `uses:` it would NOT have a compiler installed"
    );
}

/// Is a job switched off? `if: false` (a bool) or `if: 'false'` / `${{ false }}`.
fn job_is_disabled(job: &serde_yaml::Value) -> bool {
    match job.get("if") {
        Some(serde_yaml::Value::Bool(b)) => !b,
        Some(serde_yaml::Value::String(s)) => {
            let t = s
                .trim()
                .trim_start_matches("${{")
                .trim_end_matches("}}")
                .trim();
            t == "false"
        }
        _ => false,
    }
}

/// The release workflow IS the full suite (CLAUDE.md §0.2.1): every tier the
/// repo knows how to run, in a job that is enabled and that `release:` waits on.
///
/// # Why this exists
///
/// On 2026-09-13 an interim "lean release" switched the workspace tests, the T1
/// harness, both T2 jobs and all falsifier jobs off with `if: false`, trusting
/// the per-commit `ci-green` and the nightly instead. `ci-green` runs a light
/// subset of T1 and no T2 at all, so that was the v0.21.0 gap reopened — and
/// `the_release_gate_runs_the_full_tier_suite` stayed green, because it read the
/// `run:` bodies of disabled jobs. The browser tier, the e2e scripts, the doc
/// examples and `go test -race` had never been in the release workflow at all.
///
/// # What this asserts
///
/// 1. No `gate-*` job is disabled.
/// 2. Every `gate-*` job is in `release: needs:` (else it can fail after the
///    Release is published).
/// 3. The enabled `gate-*` jobs' `run:` bodies invoke every piece of the suite.
/// 4. Every falsifier invocation passes `--all` (a release carries nothing from
///    the checked-in ledger), and the union of their `--only` lists is every
///    registered gate bar the self-tests.
#[test]
fn the_release_workflow_is_the_full_suite() {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    let text = std::fs::read_to_string(root.join(".github/workflows/release.yml"))
        .expect("read release.yml");
    let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("release.yml parses");
    let jobs = doc
        .get("jobs")
        .and_then(|j| j.as_mapping())
        .expect("`jobs` mapping");

    let gate_jobs: Vec<(&str, &serde_yaml::Value)> = jobs
        .iter()
        .filter_map(|(k, v)| k.as_str().map(|n| (n, v)))
        .filter(|(n, _)| n.starts_with("gate-"))
        .collect();
    assert!(
        gate_jobs.len() >= 10,
        "found {} gate-* jobs",
        gate_jobs.len()
    );

    // 1. none disabled
    let disabled: Vec<&str> = gate_jobs
        .iter()
        .filter(|(_, v)| job_is_disabled(v))
        .map(|(n, _)| *n)
        .collect();
    assert!(
        disabled.is_empty(),
        "release gate job(s) switched off with `if: false`: {disabled:?} — the release \
         gate is the FULL suite (CLAUDE.md §0.2.1); a disabled job gates nothing"
    );

    // 2. all in release.needs
    let needs: Vec<String> = jobs
        .get(serde_yaml::Value::from("release"))
        .and_then(|r| r.get("needs"))
        .and_then(|n| n.as_sequence())
        .expect("release: needs: is a list")
        .iter()
        .filter_map(|v| v.as_str().map(str::to_string))
        .collect();
    let unneeded: Vec<&str> = gate_jobs
        .iter()
        .map(|(n, _)| *n)
        .filter(|n| !needs.iter().any(|x| x == n))
        .collect();
    assert!(
        unneeded.is_empty(),
        "gate job(s) not in `release: needs:` — the Release can publish while they \
         are red: {unneeded:?}"
    );

    // 3. every piece of the suite is invoked
    let mut runs: Vec<String> = Vec::new();
    for (_, job) in &gate_jobs {
        for step in job
            .get("steps")
            .and_then(|s| s.as_sequence())
            .into_iter()
            .flatten()
        {
            if let Some(r) = step.get("run").and_then(|r| r.as_str()) {
                runs.push(r.to_string());
            }
            // An action that runs a command for the step (the Android
            // emulator runner's `script`) counts as the step's command.
            if let Some(r) = step
                .get("with")
                .and_then(|w| w.get("script"))
                .and_then(|r| r.as_str())
            {
                runs.push(r.to_string());
            }
        }
    }
    let hay = runs.join("\n");
    let required: &[(&str, &str)] = &[
        ("cargo test --workspace", "every crate's tests"),
        ("-- --ignored", "the heavy real-DB `#[ignore]`d leg"),
        (
            "--tier t1",
            "the T1 harness (conformance, verify-cli, sky-verify, lsp, ...)",
        ),
        ("--tier t2", "the T2 behaviour corpus"),
        ("--tier t3", "the T3 Postgres app tier"),
        ("--tier t4", "the T4 pre-release tier"),
        ("--verify-falsifiers", "the falsifier proofs"),
        ("denominators --check", "census: denominators"),
        ("coverage-ledger --check", "census: coverage ledger"),
        ("config-surface --check", "census: config surface"),
        ("config-migration --check", "census: config migration"),
        ("build-run --all", "the codegen build of every example"),
        ("coerce-floor", "the runtime-narrowing floor"),
        ("scripts/example-sweep.sh", "the full clean-slate sweep"),
        ("scripts/verify-all-web.sh", "the browser tier"),
        ("scripts/doc-examples.sh", "the live-docs examples"),
        (
            "go-test-shard.sh",
            "the Go runtime under the race detector (sharded by test; every shard \
             runs: go_test_shards_run_every_shard_once_in_a_gating_job)",
        ),
        // Every `scripts/*-e2e.sh` is required by the rule in
        // `e2e_scripts_are_gated.rs`, not by a hand list that cannot notice a
        // new script (G-1: nav-e2e ran nowhere for a whole release).
        (
            "--test native_shell_flow -- --ignored",
            "the native shells on macOS: iOS simulator smoke + Android release",
        ),
        (
            "--test native_shell_flow android_emulator -- --ignored",
            "the Android emulator gate: Keystore, scanner and biometrics on a running emulator",
        ),
    ];
    let missing: Vec<String> = required
        .iter()
        .filter(|(inv, _)| !hay.contains(inv))
        .map(|(inv, why)| format!("`{inv}` — {why}"))
        .collect();
    assert!(
        missing.is_empty(),
        "release.yml's enabled gate-* jobs do not run:\n  {}",
        missing.join("\n  ")
    );
    assert!(
        !hay.lines()
            .any(|l| l.contains("example-sweep.sh") && l.contains("--build-only")),
        "the release sweep must RUN every example, not only build it"
    );

    // 4. falsifier invocations: --all, and together they cover the registry.
    let registry = std::fs::read_to_string(root.join("rust/crates/xtask/src/harness/registry.rs"))
        .expect("read the registry");
    let mut registered: Vec<(String, String)> = Vec::new(); // (name, tier)
    let mut name: Option<String> = None;
    for line in registry.lines() {
        let t = line.trim();
        if let Some(rest) = t.strip_prefix("name: \"") {
            name = rest.split('"').next().map(str::to_string);
        } else if let Some(rest) = t.strip_prefix("tier: Tier::") {
            if let Some(n) = name.take() {
                registered.push((n, rest.trim_end_matches(',').to_string()));
            }
        }
    }
    assert!(
        registered.len() > 30,
        "registry parse found {}",
        registered.len()
    );

    let mut covered: Vec<String> = Vec::new();
    for r in &runs {
        for line in r.lines().filter(|l| l.contains("--verify-falsifiers")) {
            assert!(
                line.contains("--all"),
                "a release falsifier run must pass `--all` — it must carry nothing \
                 from the checked-in ledger: {line}"
            );
            let toks: Vec<&str> = line.split_whitespace().collect();
            if let Some(i) = toks.iter().position(|t| *t == "--only") {
                covered.extend(toks[i + 1].split(',').map(str::to_string));
            }
        }
    }
    let uncovered: Vec<&str> = registered
        .iter()
        .filter(|(_, tier)| tier != "SelfTest")
        .map(|(n, _)| n.as_str())
        .filter(|n| !covered.iter().any(|c| c == n))
        .collect();
    assert!(
        uncovered.is_empty(),
        "registered gate(s) whose falsifier proof no release job re-establishes: \
         {uncovered:?} — add each to one of the gate-falsifiers-* `--only` lists"
    );

    // 4b. A gate's proof may be split across jobs by `--mutation` (the corpus
    //     gate: one job per mutation). Then the union of the mutations those
    //     lines apply must be every mutation the gate declares, or half the
    //     proof would run nowhere while each job reports PROVEN.
    let mut declared: std::collections::BTreeMap<String, Vec<String>> = Default::default();
    let mut current: Option<String> = None;
    for line in registry.lines() {
        let t = line.trim();
        if let Some(rest) = t.strip_prefix("name: \"") {
            current = rest.split('"').next().map(str::to_string);
        } else if let Some(rest) = t.strip_prefix("id: \"") {
            if let (Some(g), Some(id)) = (&current, rest.split('"').next()) {
                declared.entry(g.clone()).or_default().push(id.to_string());
            }
        }
    }
    assert!(
        declared.get("corpus").is_some_and(|m| m.len() >= 2),
        "registry mutation parse found {:?} for corpus",
        declared.get("corpus")
    );
    let mut applied: std::collections::BTreeMap<String, Vec<String>> = Default::default();
    for r in &runs {
        for line in r.lines().filter(|l| l.contains("--verify-falsifiers")) {
            let toks: Vec<&str> = line.split_whitespace().collect();
            let list = |flag: &str| -> Vec<String> {
                toks.iter()
                    .position(|t| *t == flag)
                    .map(|i| toks[i + 1].split(',').map(str::to_string).collect())
                    .unwrap_or_default()
            };
            let filter = list("--mutation");
            for gate in list("--only") {
                let all = declared.get(&gate).cloned().unwrap_or_default();
                let named: Vec<String> =
                    all.iter().filter(|m| filter.contains(m)).cloned().collect();
                let ran = if named.is_empty() { all } else { named };
                applied.entry(gate).or_default().extend(ran);
            }
        }
    }
    let mut half_proven = Vec::new();
    for (gate, ids) in &declared {
        if !covered.iter().any(|c| c == gate) {
            continue;
        }
        let missing: Vec<&String> = ids
            .iter()
            .filter(|id| !applied.get(gate).is_some_and(|a| a.contains(id)))
            .collect();
        if !missing.is_empty() {
            half_proven.push(format!("{gate}: {missing:?}"));
        }
    }
    assert!(
        half_proven.is_empty(),
        "declared mutation(s) that no release falsifier job applies:\n  {}",
        half_proven.join("\n  ")
    );

    // 5. The T1 harness runs in shards (`--tier t1 --only <list>`). A shard list
    //    is hand-written, so a gate added to T1 later would run in NO release job
    //    and the release would look green without it. A `--tier t1` line with no
    //    `--only` runs every T1 gate and covers them all.
    let mut t1_all = false;
    let mut t1_covered: Vec<String> = Vec::new();
    for r in &runs {
        for line in r
            .lines()
            .filter(|l| l.contains("--tier t1") && !l.contains("--verify-falsifiers"))
        {
            let toks: Vec<&str> = line.split_whitespace().collect();
            match toks.iter().position(|t| *t == "--only") {
                Some(i) => t1_covered.extend(toks[i + 1].split(',').map(str::to_string)),
                None => t1_all = true,
            }
        }
    }
    if !t1_all {
        let t1_uncovered: Vec<&str> = registered
            .iter()
            .filter(|(_, tier)| tier == "T1")
            .map(|(n, _)| n.as_str())
            .filter(|n| !t1_covered.iter().any(|c| c == n))
            .collect();
        assert!(
            t1_uncovered.is_empty(),
            "T1 gate(s) that no release T1 shard runs: {t1_uncovered:?} — add each to \
             one of the gate-t1-* `--only` lists"
        );
    }
}

/// The committed console_app Go must be checked against its Sky source on every
/// pull request, in a job the required check fans in.
///
/// # Why this exists
///
/// `runtime-go/rt/console_app/main.go` is generated from
/// `sky-bundled/console/src` by `scripts/regenerate-console.sh` and committed.
/// The script header said "CI runs this + `git diff --exit-code
/// runtime-go/rt/console_app/`". No workflow did. The Std.App migration of the
/// console changed the Sky source without a regeneration, and the file drifted
/// for a month: the next regeneration was a 6,298-line diff, and the result did
/// not build, because the hand-written glue (`register_v3.go`) named a binding
/// (`Main_viewWrapped`) the source no longer defined.
///
/// # What this asserts
///
/// In one `rust-ci.yml` job that `ci-green` needs, and that is not disabled,
/// a step runs `scripts/regenerate-console.sh` with `SKY_REGEN_SKIP_BUILD=1`
/// (the compiler the job built, not a stale `sky-out/sky`), and the same or a
/// later step runs `git diff --exit-code runtime-go/rt/console_app`. Neither
/// step may be `continue-on-error` or conditional. It also asserts the script
/// still builds the regenerated package, so glue that names a symbol the
/// compiler no longer emits fails the regeneration and not a user build.
/// Parsed, never grepped as raw text: a comment must not vouch for a step.
#[test]
fn the_console_drift_check_runs_on_a_pull_request() {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    let text = std::fs::read_to_string(root.join(".github/workflows/rust-ci.yml"))
        .expect("read rust-ci.yml");
    let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("rust-ci.yml parses");
    let jobs = doc
        .get("jobs")
        .and_then(|j| j.as_mapping())
        .expect("`jobs` mapping");
    let fanned: Vec<String> = jobs
        .get(serde_yaml::Value::from("ci-green"))
        .and_then(|g| g.get("needs"))
        .and_then(|n| n.as_sequence())
        .map(|s| {
            s.iter()
                .filter_map(|v| v.as_str())
                .map(str::to_string)
                .collect()
        })
        .expect("ci-green declares a `needs` list");

    let step_is_unconditional = |step: &serde_yaml::Value| {
        let soft = matches!(
            step.get("continue-on-error"),
            Some(serde_yaml::Value::Bool(true))
        ) || step
            .get("continue-on-error")
            .and_then(|v| v.as_str())
            .is_some_and(|s| s.trim() == "true");
        !soft && step.get("if").is_none()
    };

    let mut found_in: Option<String> = None;
    for (name, job) in jobs {
        let Some(name) = name.as_str() else { continue };
        if !fanned.iter().any(|f| f == name) || job_is_disabled(job) {
            continue;
        }
        let Some(steps) = job.get("steps").and_then(|s| s.as_sequence()) else {
            continue;
        };
        let mut regen_at: Option<usize> = None;
        for (i, step) in steps.iter().enumerate() {
            if !step_is_unconditional(step) {
                continue;
            }
            let run = step.get("run").and_then(|r| r.as_str()).unwrap_or("");
            // Executable lines only — a `#` comment inside a `run:` body must
            // not vouch for the invocation either.
            let code: Vec<&str> = run
                .lines()
                .map(str::trim)
                .filter(|l| !l.starts_with('#'))
                .collect();
            if regen_at.is_none()
                && code.iter().any(|l| {
                    l.contains("scripts/regenerate-console.sh")
                        && l.contains("SKY_REGEN_SKIP_BUILD=1")
                })
            {
                regen_at = Some(i);
            }
            if regen_at.is_some_and(|r| r <= i)
                && code
                    .iter()
                    .any(|l| l.contains("git diff --exit-code runtime-go/rt/console_app"))
            {
                found_in = Some(name.to_string());
                break;
            }
        }
        if found_in.is_some() {
            break;
        }
    }
    assert!(
        found_in.is_some(),
        "no `rust-ci.yml` job that `ci-green` needs runs \
         `SKY_REGEN_SKIP_BUILD=1 bash scripts/regenerate-console.sh` followed by \
         `git diff --exit-code runtime-go/rt/console_app/` as unconditional steps. \
         Without it the committed console_app Go drifts from \
         sky-bundled/console/src unnoticed, which is how it went stale for a \
         month and stopped building on regeneration."
    );

    let script = std::fs::read_to_string(root.join("scripts/regenerate-console.sh"))
        .expect("read scripts/regenerate-console.sh");
    assert!(
        script.contains("go build ./rt/console_app"),
        "scripts/regenerate-console.sh no longer builds the regenerated \
         console_app package. The glue in register_v3.go names generated \
         symbols; without the build a regeneration can leave a package that \
         does not compile, and the drift check alone would bless it."
    );
}

/// Every nextest `--partition slice:k/N` split of the `sky` crate, in either
/// workflow, runs ALL N slices exactly once, in jobs that gate the result
/// (`ci-green` needs them in rust-ci.yml; `release` needs them in release.yml).
///
/// # Why this exists
///
/// The sky crate's tests are split by test across parallel jobs (rust-ci
/// `test-sky` .. `test-sky-9`, release `gate-core-sky-1..9`). Each job's
/// command names its own slice. Deleting one job, or two jobs naming the same
/// slice, leaves a slice of tests that no job runs, and every remaining job
/// still reports green. This makes the split disjoint-and-total a checked
/// property instead of a hand-maintained one.
#[test]
fn every_sky_nextest_split_runs_every_slice_once_in_a_gating_job() {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    for (file, fan_in) in [("rust-ci.yml", "ci-green"), ("release.yml", "release")] {
        let text = std::fs::read_to_string(root.join(".github/workflows").join(file))
            .expect("read workflow");
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parses");
        let jobs = doc.get("jobs").and_then(|j| j.as_mapping()).expect("jobs");
        let gating: Vec<String> = jobs
            .get(serde_yaml::Value::from(fan_in))
            .and_then(|g| g.get("needs"))
            .and_then(|n| n.as_sequence())
            .map(|s| {
                s.iter()
                    .filter_map(|v| v.as_str())
                    .map(str::to_string)
                    .collect()
            })
            .unwrap_or_default();
        let mut slices: Vec<(u32, u32, String)> = Vec::new();
        for (name, job) in jobs {
            let Some(name) = name.as_str() else { continue };
            if job_is_disabled(job) {
                continue;
            }
            for step in job
                .get("steps")
                .and_then(|s| s.as_sequence())
                .into_iter()
                .flatten()
            {
                let run = step.get("run").and_then(|r| r.as_str()).unwrap_or("");
                for line in run.lines().map(str::trim).filter(|l| !l.starts_with('#')) {
                    if !(line.contains("nextest run") && line.contains("-p sky")) {
                        continue;
                    }
                    let toks: Vec<&str> = line.split_whitespace().collect();
                    let Some(i) = toks.iter().position(|t| *t == "--partition") else {
                        panic!("{file}: job `{name}` runs the sky crate under nextest without a partition, alongside sliced jobs? `{line}`");
                    };
                    let spec = toks[i + 1];
                    let rest = spec
                        .strip_prefix("slice:")
                        .unwrap_or_else(|| panic!("{file}: `{name}` uses `{spec}`; the sky crate is split with `slice:k/N`"));
                    let (k, n) = rest.split_once('/').expect("k/N");
                    slices.push((
                        k.parse().expect("k"),
                        n.parse().expect("N"),
                        name.to_string(),
                    ));
                }
            }
        }
        assert!(
            !slices.is_empty(),
            "{file}: no sliced sky nextest run found — the parse is wrong"
        );
        let n = slices[0].1;
        assert!(
            slices.iter().all(|(_, m, _)| *m == n),
            "{file}: sky slices disagree on N: {slices:?}"
        );
        let mut ks: Vec<u32> = slices.iter().map(|(k, _, _)| *k).collect();
        ks.sort();
        assert_eq!(
            ks,
            (1..=n).collect::<Vec<u32>>(),
            "{file}: sky slices must be 1..={n}, each exactly once: {slices:?}"
        );
        let ungated: Vec<&String> = slices
            .iter()
            .map(|(_, _, j)| j)
            .filter(|j| !gating.contains(j))
            .collect();
        assert!(
            ungated.is_empty(),
            "{file}: sky slice job(s) {ungated:?} are not in `{fan_in}: needs:`"
        );
    }
}

/// Every Go module under `tools/` that has a `_test.go` file is tested by a
/// `go test` step in BOTH the release workflow (the merge/release gate) and
/// `rust-ci.yml` (per commit). `tools/sky-ffi-inspect/sky3_test.go` ran in no
/// workflow at all, and nothing noticed.
#[test]
fn every_go_test_package_under_tools_runs_in_ci() {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    fn has_go_test(dir: &std::path::Path) -> bool {
        std::fs::read_dir(dir)
            .into_iter()
            .flatten()
            .flatten()
            .any(|e| {
                let p = e.path();
                if p.is_dir() {
                    !p.join("go.mod").exists() && has_go_test(&p)
                } else {
                    p.to_string_lossy().ends_with("_test.go")
                }
            })
    }
    let mut modules: Vec<String> = std::fs::read_dir(root.join("tools"))
        .expect("read tools/")
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.join("go.mod").exists() && has_go_test(p))
        .map(|p| format!("tools/{}", p.file_name().unwrap().to_string_lossy()))
        .collect();
    modules.sort();
    assert!(
        modules.iter().any(|m| m == "tools/sky-ffi-inspect"),
        "the scan finds no Go test module under tools/ ({modules:?}): the scan is wrong"
    );
    for wf in ["release.yml", "rust-ci.yml"] {
        let text = std::fs::read_to_string(root.join(".github/workflows").join(wf))
            .unwrap_or_else(|e| panic!("read {wf}: {e}"));
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parses");
        let mut runs: Vec<String> = Vec::new();
        for (_, job) in doc["jobs"].as_mapping().expect("jobs").iter() {
            if job_is_disabled(job) {
                continue;
            }
            for step in job["steps"].as_sequence().into_iter().flatten() {
                let run = step["run"].as_str().unwrap_or_default();
                if run.contains("go test") {
                    let wd = step["working-directory"].as_str().unwrap_or_default();
                    runs.push(format!("{wd}\n{run}"));
                }
            }
        }
        let missing: Vec<&String> = modules
            .iter()
            .filter(|m| !runs.iter().any(|r| r.contains(m.as_str())))
            .collect();
        assert!(
            missing.is_empty(),
            "{wf}: no enabled job runs `go test` in {missing:?}. Add a step \
             (`cd <module> && go test ./...`) to a gating job."
        );
    }
}

/// The browser-tier jobs whose steps each run long e2e scripts, as
/// (workflow file, job id). The release splits the tier across four jobs.
const BROWSER_TIER_JOBS: [(&str, &str); 5] = [
    ("release.yml", "gate-web-1"),
    ("release.yml", "gate-web-2"),
    ("release.yml", "gate-web-3"),
    ("release.yml", "gate-web-4"),
    ("nightly-sweep.yml", "web-runtime"),
];

/// Does this `run:` block call a gate script (`scripts/<name>.sh`)?
fn runs_a_gate_script(run: &str) -> bool {
    run.split_whitespace().any(|w| {
        w.starts_with("scripts/")
            && w.ends_with(".sh")
            && !w.starts_with("scripts/ci/")
            && !w.starts_with("scripts/lib/")
    })
}

/// The `timeout-minutes` integer of a job or a step, if it has one.
fn timeout_minutes(v: &serde_yaml::Value) -> Option<u64> {
    v.get("timeout-minutes").and_then(|t| t.as_u64())
}

/// Every step of a browser-tier job that runs a gate script has its OWN
/// `timeout-minutes`, strictly below the job's.
///
/// Release run 36942837671 (head 7d6c883c): one script of `gate-web` step 7
/// ("Sky.Spa wasm client e2e") hung. Nothing bounded the step, so it ran
/// until the JOB limit cancelled everything at 70 min. A cancelled job keeps
/// no step log and uploads no evidence, so the failure named neither the
/// script nor the stage. With a step bound the same hang fails its own named
/// step, keeps its log, and the `if: failure()` evidence upload still runs.
#[test]
fn every_browser_tier_gate_step_has_its_own_bound() {
    let dir = PathBuf::from(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../.github/workflows"
    ));
    let mut missing = Vec::new();
    let mut per_file: std::collections::BTreeMap<&str, usize> = Default::default();
    for (file, job_id) in BROWSER_TIER_JOBS {
        let text = std::fs::read_to_string(dir.join(file))
            .unwrap_or_else(|e| panic!("cannot read {file}: {e}"));
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parses");
        let job = doc
            .get("jobs")
            .and_then(|j| j.get(job_id))
            .unwrap_or_else(|| panic!("{file}: job `{job_id}` is gone; update BROWSER_TIER_JOBS"));
        let job_bound = timeout_minutes(job)
            .unwrap_or_else(|| panic!("{file}: job `{job_id}` has no timeout-minutes"));
        let steps = job
            .get("steps")
            .and_then(|s| s.as_sequence())
            .unwrap_or_else(|| panic!("{file}: job `{job_id}` has no steps"));
        let mut gate_steps = 0;
        for step in steps {
            let Some(run) = step.get("run").and_then(|r| r.as_str()) else {
                continue;
            };
            if !runs_a_gate_script(run) {
                continue;
            }
            gate_steps += 1;
            let name = step
                .get("name")
                .and_then(|n| n.as_str())
                .unwrap_or("<unnamed>");
            match timeout_minutes(step) {
                Some(m) if m < job_bound => {}
                Some(m) => missing.push(format!(
                    "{file} {job_id} `{name}`: timeout-minutes {m} is not below the job's {job_bound}"
                )),
                None => missing.push(format!("{file} {job_id} `{name}`: no timeout-minutes")),
            }
        }
        assert!(
            gate_steps >= 1,
            "{file} {job_id}: no gate-script steps found; the scanner is wrong"
        );
        *per_file.entry(file).or_default() += gate_steps;
    }
    for (file, n) in &per_file {
        assert!(
            *n >= 10,
            "{file}: only {n} browser-tier gate-script steps found; the scanner is wrong"
        );
    }
    assert!(
        missing.is_empty(),
        "a browser-tier step that runs a gate script needs its own timeout-minutes \
         (about twice its slowest green run), below the job's, so a hang fails \
         the named step with its log:\n  {}",
        missing.join("\n  ")
    );
}

/// Entering the Nix dev shell must not write the environment to disk
/// (v0.27.7). stdenv's `dumpVars` writes every exported variable to
/// `$NIX_BUILD_TOP/env-vars` when a shell is entered; for `nix-shell -A shell`
/// that was the caller's TMPDIR (or `/tmp` on macOS), so a developer's secrets
/// landed in a plain file in a shared directory. This pins the three parts of
/// the fix without needing Nix: the dev shell sets `noDumpEnvVars`, the CI
/// check runs Nix in a private TMPDIR it removes on exit, and it runs
/// `scripts/ci/nix-dev-shell-env-check.sh`, which enters both dev shells and
/// fails if the environment reaches a file (that one needs Nix, and runs in
/// the jobs the test below pins).
#[test]
fn the_nix_dev_shell_writes_no_environment_to_disk() {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    let nix = std::fs::read_to_string(root.join("default.nix")).expect("read default.nix");
    let shell_at = nix
        .find("mkShell {")
        .expect("default.nix: the dev shell is a `mkShell { ... }`");
    let shell = &nix[shell_at..];
    let shell = &shell[..shell.find("shellHook").unwrap_or(shell.len())];
    assert!(
        shell.contains("noDumpEnvVars = true;"),
        "default.nix: the dev shell must set `noDumpEnvVars = true;`, or entering it \
         writes the whole environment to $TMPDIR/env-vars"
    );

    let check = std::fs::read_to_string(root.join("scripts/ci/nix-build-check.sh"))
        .expect("read scripts/ci/nix-build-check.sh");
    for needle in [
        "PRIVATE_TMPDIR=\"$(mktemp -d",
        "chmod 700 \"$PRIVATE_TMPDIR\"",
        "trap 'rm -rf \"$PRIVATE_TMPDIR\"' EXIT",
        "export TMPDIR=\"$PRIVATE_TMPDIR\"",
        "scripts/ci/nix-dev-shell-env-check.sh",
    ] {
        assert!(
            check.contains(needle),
            "scripts/ci/nix-build-check.sh no longer contains `{needle}`: every Nix call \
             must run in a private TMPDIR removed on exit, and the dev-shell dump check \
             must run"
        );
    }
    let first_nix_call = check
        .find("nix-build --out-link")
        .expect("nix-build-check.sh builds with nix-build");
    let private_at = check.find("export TMPDIR=").unwrap();
    assert!(
        private_at < first_nix_call,
        "scripts/ci/nix-build-check.sh must switch to its private TMPDIR before its first Nix call"
    );

    let env_check = std::fs::read_to_string(root.join("scripts/ci/nix-dev-shell-env-check.sh"))
        .expect("read scripts/ci/nix-dev-shell-env-check.sh");
    for needle in [
        "nix-shell -A shell",
        "develop -c true",
        "env-vars",
        "SKY_DUMMY_SECRET=",
    ] {
        assert!(
            env_check.contains(needle),
            "scripts/ci/nix-dev-shell-env-check.sh no longer contains `{needle}`"
        );
    }
}

/// The Nix package is built through both entry points (the stable `nix-build`
/// of `default.nix` and the flake's `nix build .#sky`) by the nightly and by a
/// release gate job (GitHub issue #216).
///
/// `scripts/ci/nix-build-check.sh` holds the checks: both builds, both dev
/// shells, `sky --version` against the version Nix computed (and the tag, on a
/// tag), and `sky-ffi-inspect` on a real Go package. This test pins that the
/// script exists, still builds BOTH entry points, and is invoked by an enabled
/// job in each workflow; in `release.yml` that job is a `gate-*` job, so
/// `the_release_workflow_is_the_full_suite` holds it in `release: needs:`.
#[test]
fn the_nix_entry_points_are_built_by_the_nightly_and_the_release_gate() {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    let script = std::fs::read_to_string(root.join("scripts/ci/nix-build-check.sh"))
        .expect("read scripts/ci/nix-build-check.sh");
    for needle in [
        "nix-build --out-link",
        "build .#sky",
        "--version",
        "sky-ffi-inspect",
    ] {
        assert!(
            script.contains(needle),
            "scripts/ci/nix-build-check.sh no longer contains `{needle}`: it must build \
             both Nix entry points and check each binary's version and inspector"
        );
    }

    for (file, prefix) in [("nightly-sweep.yml", ""), ("release.yml", "gate-")] {
        let text = std::fs::read_to_string(root.join(".github/workflows").join(file))
            .unwrap_or_else(|e| panic!("read {file}: {e}"));
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parses");
        let runners: Vec<&str> = doc["jobs"]
            .as_mapping()
            .expect("jobs")
            .iter()
            .filter(|(_, job)| !job_is_disabled(job))
            .filter(|(_, job)| {
                job["steps"].as_sequence().into_iter().flatten().any(|s| {
                    s["run"]
                        .as_str()
                        .is_some_and(|r| r.contains("scripts/ci/nix-build-check.sh"))
                })
            })
            .filter_map(|(name, _)| name.as_str())
            .filter(|name| name.starts_with(prefix))
            .collect();
        assert!(
            !runners.is_empty(),
            "{file}: no enabled {prefix}* job runs scripts/ci/nix-build-check.sh, so \
             the Nix package can break unseen (GitHub issue #216)"
        );
    }
}

/// Every job that runs the `sky` crate's tests installs the `brotli` tool.
///
/// The precompress tests (`sky` unit tests, `spa_target_flow`'s `--ignored`
/// leg) compare Sky's `.br` assets with brotli's own output, through
/// `live_gate::required(Need::Brotli, …)`: without the tool they FAIL (live
/// tests never skip silently). A job that runs them without installing it goes
/// red for a reason that is not the code. The release gate jobs get it from the
/// `gate-setup` action (`postgres: 'true'`); the others install it themselves.
#[test]
fn every_job_running_the_sky_tests_installs_brotli() {
    let action = std::fs::read_to_string(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../.github/actions/gate-setup/action.yml"
    ))
    .expect("read gate-setup");
    let setup_has_brotli = action.contains("brotli");
    let mut bad = Vec::new();
    for path in workflows() {
        let text = std::fs::read_to_string(&path).expect("read workflow");
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parsed above");
        let Some(jobs) = doc.get("jobs").and_then(|j| j.as_mapping()) else {
            continue;
        };
        for (name, job) in jobs {
            let steps: Vec<&serde_yaml::Value> = job
                .get("steps")
                .and_then(|s| s.as_sequence())
                .into_iter()
                .flatten()
                .collect();
            let runs: Vec<&str> = steps
                .iter()
                .filter_map(|s| s.get("run").and_then(|r| r.as_str()))
                .collect();
            let runs_sky_tests = runs.iter().any(|r| {
                r.lines().any(|l| {
                    (l.contains("nextest run -p sky") || l.contains("cargo test -p sky"))
                        && !l.contains("--test native_shell_flow")
                        && !l.contains("--no-run")
                })
            });
            if !runs_sky_tests {
                continue;
            }
            let installs = runs.iter().any(|r| r.contains("brotli"));
            let via_setup = setup_has_brotli
                && steps.iter().any(|s| {
                    s.get("uses").and_then(|u| u.as_str()) == Some("./.github/actions/gate-setup")
                        && s.get("with")
                            .and_then(|w| w.get("postgres"))
                            .and_then(|p| p.as_str())
                            == Some("true")
                });
            if !installs && !via_setup {
                bad.push(format!(
                    "{}: {}",
                    path.file_name().unwrap().to_string_lossy(),
                    name.as_str().unwrap_or("?")
                ));
            }
        }
    }
    assert!(
        bad.is_empty(),
        "these jobs run the sky crate's tests without installing brotli (the \
         precompress tests then fail on Need::Brotli):\n  {}",
        bad.join("\n  ")
    );
}

/// The full build-speed measure, as the nightly runs it: the app at both
/// sizes and examples/13-skyshop, every scenario, every budgeted key.
const FULL_BENCH: &str = "bash scripts/bench-build.sh --reps 3 --cases \"app skyshop\" \
     --out build-perf.json --write-budget build-budget.proposed.json \
     --budget docs/perf/build-budget.json";

/// The per-push and pull-request measure in rust-ci.yml: the 50-field app
/// against its own budget keys, one target per job (`build-perf` measures
/// `web`, `build-perf-2` measures `web:app`; together they are the script's
/// default targets, so the per-push scope is unchanged by the split).
/// `--budget-subset` requires every budgeted key of the measured case and
/// still fails on a rise.
const SUBSET_BENCH_JOBS: [(&str, &str); 2] = [("build-perf", "web"), ("build-perf-2", "web:app")];

fn subset_bench(target: &str) -> String {
    format!(
        "bash scripts/bench-build.sh --reps 3 --cases app --sizes 50 --targets {target} \
         --out build-perf.json --budget docs/perf/build-budget.json --budget-subset"
    )
}

/// The only condition rust-ci.yml's measure may carry: the in-job path
/// filter. Anything else (an event or branch test, `false`) is a skip.
const RUST_CI_BENCH_IF: &str = "steps.paths.outputs.run == 'true'";

/// The `build-perf` job of a parsed workflow, or why it is unusable.
fn build_perf_job<'a>(
    file: &str,
    doc: &'a serde_yaml::Value,
) -> Result<&'a serde_yaml::Value, String> {
    named_build_perf_job(file, doc, "build-perf")
}

/// The named build-speed job of a parsed workflow, or why it is unusable.
fn named_build_perf_job<'a>(
    file: &str,
    doc: &'a serde_yaml::Value,
    name: &str,
) -> Result<&'a serde_yaml::Value, String> {
    let job = &doc["jobs"][name];
    if job.is_null() || job_is_disabled(job) {
        return Err(format!("{file}: no enabled {name} job"));
    }
    Ok(job)
}

/// The steps of a job that run scripts/bench-build.sh.
fn bench_steps(job: &serde_yaml::Value) -> Vec<&serde_yaml::Value> {
    job["steps"]
        .as_sequence()
        .into_iter()
        .flatten()
        .filter(|s| {
            s["run"]
                .as_str()
                .is_some_and(|r| r.contains("scripts/bench-build.sh"))
        })
        .collect()
}

/// What is wrong with the nightly's `build-perf` job: it must run exactly
/// the full measure, in one step with no condition of its own.
fn nightly_build_perf_problems(doc: &serde_yaml::Value) -> Vec<String> {
    let file = "nightly-sweep.yml";
    let job = match build_perf_job(file, doc) {
        Ok(j) => j,
        Err(e) => return vec![e],
    };
    let mut problems = Vec::new();
    let benches = bench_steps(job);
    if benches.len() != 1 {
        problems.push(format!(
            "{file}: build-perf has {} bench-build steps, want 1",
            benches.len()
        ));
    }
    for s in benches {
        let run = s["run"].as_str().unwrap_or("").trim();
        if run != FULL_BENCH {
            problems.push(format!(
                "{file}: build-perf runs `{run}`, want exactly `{FULL_BENCH}`"
            ));
        }
        if !s["if"].is_null() {
            problems.push(format!(
                "{file}: the full build-perf step carries `if: {:?}`; it must run every night",
                s["if"]
            ));
        }
    }
    problems
}

/// What is wrong with rust-ci.yml's `build-perf` job: on every event it must
/// run exactly the 50-field subset, gated only by the in-job path filter, and
/// that filter must say `run=true` when a build-speed input changes.
fn rust_ci_build_perf_problems(doc: &serde_yaml::Value) -> Vec<String> {
    let file = "rust-ci.yml";
    let mut problems = Vec::new();
    for (name, target) in SUBSET_BENCH_JOBS {
        let want = subset_bench(target);
        let job = match named_build_perf_job(file, doc, name) {
            Ok(j) => j,
            Err(e) => {
                problems.push(e);
                continue;
            }
        };
        if !job["if"].is_null() {
            problems.push(format!(
                "{file}: {name} carries a job-level `if: {:?}`; ci-green treats a \
                 skipped job as a failure, and the path filter lives inside the job",
                job["if"]
            ));
        }
        let benches = bench_steps(job);
        if benches.len() != 1 {
            problems.push(format!(
                "{file}: {name} has {} bench-build steps, want 1 (the full measure \
                 belongs in the nightly; it does not fit the T1 tier budget)",
                benches.len()
            ));
        }
        for s in benches {
            let run = s["run"].as_str().unwrap_or("").trim();
            if run != want {
                problems.push(format!(
                    "{file}: {name} runs `{run}`, want exactly `{want}`"
                ));
            }
            let cond = s["if"].as_str().map(str::trim);
            if cond != Some(RUST_CI_BENCH_IF) {
                problems.push(format!(
                    "{file}: the {name} measure runs under `if: {cond:?}`, want \
                     {RUST_CI_BENCH_IF:?} (the path filter only, on every event)"
                ));
            }
        }
        // The path filter must exist, set `run=true`, and match the compiler,
        // runtime, stdlib, bench script and budget. A filter that never says
        // `run=true` is a skip in disguise.
        let paths = job["steps"]
            .as_sequence()
            .into_iter()
            .flatten()
            .find(|s| s["id"].as_str() == Some("paths"));
        match paths.and_then(|s| s["run"].as_str()) {
            None => problems.push(format!("{file}: {name} has no `id: paths` step")),
            Some(run) => {
                for needle in [
                    "echo \"run=true\" >> \"$GITHUB_OUTPUT\"",
                    "rust/crates/(project|sky|codegen|lower|ty|hir|skydb)/",
                    "runtime-go/",
                    "sky-stdlib/",
                    "scripts/bench-build\\.sh$",
                    "docs/perf/build-budget\\.json$",
                ] {
                    if !run.contains(needle) {
                        problems.push(format!("{file}: the {name} path filter lacks `{needle}`"));
                    }
                }
            }
        }
    }
    problems
}

fn read_workflow(file: &str) -> serde_yaml::Value {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    let text = std::fs::read_to_string(root.join(".github/workflows").join(file))
        .unwrap_or_else(|e| panic!("read {file}: {e}"));
    serde_yaml::from_str(&text).unwrap_or_else(|e| panic!("{file} does not parse: {e}"))
}

/// The bench-build step of a job, for a mutation in the tests below.
fn bench_step_mut<'a>(doc: &'a mut serde_yaml::Value) -> &'a mut serde_yaml::Value {
    doc["jobs"]["build-perf"]["steps"]
        .as_sequence_mut()
        .expect("build-perf has steps")
        .iter_mut()
        .find(|s| {
            s["run"]
                .as_str()
                .is_some_and(|r| r.contains("scripts/bench-build.sh"))
        })
        .expect("build-perf has a bench-build step")
}

/// Per push and per pull request, the 50-field app; every night, the full set.
///
/// The full measure (both app sizes and examples/13-skyshop, every scenario)
/// takes about 30 minutes on a runner. On a push to main it made build-perf
/// the slowest setup-independent job, 1776 s against the 990 s T1 tier budget
/// (rust-ci run 38837799531). So rust-ci.yml measures the 50-field app on
/// every event, against its own budget keys, and the nightly measures the full
/// set. This pins both invocations, and pins that rust-ci's measure is
/// skipped only by its path filter.
#[test]
fn build_speed_measures_the_50_field_app_per_push_and_the_full_set_nightly() {
    let mut problems = rust_ci_build_perf_problems(&read_workflow("rust-ci.yml"));
    problems.extend(nightly_build_perf_problems(&read_workflow(
        "nightly-sweep.yml",
    )));
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

/// The nightly check above goes red on the shapes that would shrink the full
/// measure, and the rust-ci check goes red on a skip or a changed scope.
#[test]
fn the_full_build_speed_check_catches_a_shrunk_measure() {
    let nightly = read_workflow("nightly-sweep.yml");
    assert!(
        nightly_build_perf_problems(&nightly).is_empty(),
        "the nightly is not in its pinned shape"
    );
    let set_run = |d: &mut serde_yaml::Value, run: &str| {
        bench_step_mut(d)["run"] = serde_yaml::Value::String(run.into());
    };
    let mut cases: Vec<(&str, serde_yaml::Value)> = Vec::new();
    let mut d = nightly.clone();
    set_run(&mut d, &FULL_BENCH.replace("\"app skyshop\"", "app"));
    cases.push(("nightly: skyshop dropped", d));
    let mut d = nightly.clone();
    set_run(
        &mut d,
        &FULL_BENCH.replace("--reps 3", "--reps 3 --sizes 50"),
    );
    cases.push(("nightly: one app size", d));
    let mut d = nightly.clone();
    set_run(&mut d, &format!("{FULL_BENCH} --budget-subset"));
    cases.push(("nightly: --budget-subset", d));
    let mut d = nightly.clone();
    set_run(&mut d, &subset_bench("web"));
    cases.push(("nightly: the per-push subset", d));
    let mut d = nightly.clone();
    bench_step_mut(&mut d)["if"] = serde_yaml::Value::String("false".into());
    cases.push(("nightly: the step is skipped", d));
    let mut d = nightly.clone();
    d["jobs"]["build-perf"]["if"] = serde_yaml::Value::Bool(false);
    cases.push(("nightly: the job is disabled", d));
    for (what, d) in &cases {
        assert!(
            !nightly_build_perf_problems(d).is_empty(),
            "the check missed: {what}"
        );
    }

    let ci = read_workflow("rust-ci.yml");
    assert!(
        rust_ci_build_perf_problems(&ci).is_empty(),
        "rust-ci.yml is not in its pinned shape"
    );
    let mut cases: Vec<(&str, serde_yaml::Value)> = Vec::new();
    let mut d = ci.clone();
    set_run(&mut d, FULL_BENCH);
    cases.push(("rust-ci: the full measure per push", d));
    let mut d = ci.clone();
    set_run(&mut d, &subset_bench("web").replace(" --budget-subset", ""));
    cases.push(("rust-ci: no --budget-subset", d));
    let mut d = ci.clone();
    set_run(
        &mut d,
        &subset_bench("web").replace(" --budget docs/perf/build-budget.json", ""),
    );
    cases.push(("rust-ci: no --budget", d));
    let mut d = ci.clone();
    bench_step_mut(&mut d)["if"] = serde_yaml::Value::String("false".into());
    cases.push(("rust-ci: the step is skipped", d));
    let mut d = ci.clone();
    bench_step_mut(&mut d)["if"] =
        serde_yaml::Value::String(format!("{RUST_CI_BENCH_IF} && github.event_name != 'push'"));
    cases.push(("rust-ci: skipped on a push", d));
    let mut d = ci.clone();
    d["jobs"]["build-perf"]["if"] = serde_yaml::Value::String("false".into());
    cases.push(("rust-ci: the job is disabled", d));
    let mut d = ci.clone();
    for s in d["jobs"]["build-perf"]["steps"]
        .as_sequence_mut()
        .expect("steps")
        .iter_mut()
    {
        if s["id"].as_str() == Some("paths") {
            let run = s["run"].as_str().expect("paths run").replace(
                "echo \"run=true\" >> \"$GITHUB_OUTPUT\"",
                "echo \"run=false\" >> \"$GITHUB_OUTPUT\"",
            );
            s["run"] = serde_yaml::Value::String(run);
        }
    }
    cases.push(("rust-ci: the path filter never says run=true", d));
    let mut d = ci.clone();
    d["jobs"]
        .as_mapping_mut()
        .expect("jobs")
        .remove(serde_yaml::Value::from("build-perf-2"));
    cases.push(("rust-ci: the web:app half is gone", d));
    let mut d = ci.clone();
    for s in d["jobs"]["build-perf-2"]["steps"]
        .as_sequence_mut()
        .expect("steps")
        .iter_mut()
    {
        if s["run"]
            .as_str()
            .is_some_and(|r| r.contains("scripts/bench-build.sh"))
        {
            s["run"] = serde_yaml::Value::String(subset_bench("web"));
        }
    }
    cases.push(("rust-ci: both halves measure web", d));
    for (what, d) in &cases {
        assert!(
            !rust_ci_build_perf_problems(d).is_empty(),
            "the check missed: {what}"
        );
    }
}

/// Every file under `.github/` that a job runs: the workflows and the local
/// composite actions.
fn workflows_and_actions() -> Vec<PathBuf> {
    let mut out = workflows();
    let actions = PathBuf::from(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../.github/actions"
    ));
    for entry in std::fs::read_dir(&actions)
        .unwrap_or_else(|e| panic!("cannot read {}: {e}", actions.display()))
        .flatten()
    {
        for name in ["action.yml", "action.yaml"] {
            let p = entry.path().join(name);
            if p.is_file() {
                out.push(p);
            }
        }
    }
    out
}

/// Does this line name a numbered PostgreSQL package or install path
/// (`postgresql-16`, `/usr/lib/postgresql/16`, `/usr/share/postgresql/16`)?
fn names_a_postgresql_major(line: &str) -> bool {
    [
        "postgresql-",
        "/usr/lib/postgresql/",
        "/usr/share/postgresql/",
    ]
    .iter()
    .any(|prefix| {
        line.match_indices(prefix).any(|(i, _)| {
            line[i + prefix.len()..]
                .bytes()
                .next()
                .is_some_and(|b| b.is_ascii_digit())
        })
    })
}

#[test]
fn the_postgresql_major_detector_sees_every_spelling() {
    for hit in [
        "sudo apt-get install -y -qq postgresql-16 sqlite3",
        "sudo apt-get install -y -qq postgresql-18-pgvector",
        "SKY_POSTGRES_BIN: /usr/lib/postgresql/16/bin",
        "[ -f /usr/share/postgresql/16/extension/vector.control ]",
    ] {
        assert!(names_a_postgresql_major(hit), "missed: {hit}");
    }
    for miss in [
        "apt_install postgresql",
        "apt_install \"postgresql-$major-pgvector\"",
        "for d in /usr/lib/postgresql/*/bin; do",
        "image: postgres:16",
    ] {
        assert!(!names_a_postgresql_major(miss), "false hit: {miss}");
    }
}

/// Does this step resolve PostgreSQL for the steps after it?
fn resolves_postgres(step: &serde_yaml::Value) -> bool {
    let run = step.get("run").and_then(|r| r.as_str()).unwrap_or("");
    if run.contains("scripts/ci/resolve-postgres.sh") {
        return true;
    }
    step.get("uses").and_then(|u| u.as_str()) == Some("./.github/actions/gate-setup")
        && step
            .get("with")
            .and_then(|w| w.get("postgres"))
            .and_then(|p| p.as_str())
            == Some("true")
}

/// Does this run line need a real PostgreSQL on the runner? The workspace and
/// `sky` crate tests (the shared-cluster live tests, `db_*_flow`), the
/// `--ignored` real-DB leg, the rt race run (the embedded supervisor) and the
/// harness T1 / T3 tiers (Layer-2 apps) all stand up their own cluster from
/// SKY_POSTGRES_BIN.
fn needs_runner_postgres(run: &str) -> bool {
    run.lines().any(|l| {
        l.contains("cargo test --workspace")
            || l.contains("nextest run -p sky")
            || (l.contains("cargo test -p sky") && l.contains("--ignored"))
            || (l.contains("go test -race") && l.contains("./rt/"))
            || (l.contains("go-test-shard.sh") && l.contains("./rt/") && l.contains("-race"))
            || l.contains("harness --tier t1")
            || l.contains("harness --tier t3")
    })
}

/// The PostgreSQL major is a fact about the runner image, never a literal.
///
/// Every job used to `apt-get install postgresql-16` and set
/// `SKY_POSTGRES_BIN: /usr/lib/postgresql/16/bin`. That held only while
/// `ubuntu-latest` meant Ubuntu 24.04: the 26.04 image ships PostgreSQL 18 and
/// its archive has no `postgresql-16`, so every live-test job would have died
/// on an apt error the day the label moved. `scripts/ci/resolve-postgres.sh`
/// now discovers the major (the image's own, else the distribution default)
/// and exports SKY_POSTGRES_BIN, so:
///
///   * no workflow or composite action names a numbered PostgreSQL package or
///     a `/usr/lib/postgresql/<n>` path,
///   * no `env:` map sets SKY_POSTGRES_BIN (a literal would override the
///     resolver for that step), and
///   * every Linux job that runs a PostgreSQL-backed test resolves it in an
///     EARLIER step, so the test cannot reach its "PostgreSQL is missing" path.
#[test]
fn postgresql_comes_from_the_resolver_never_a_hardcoded_major() {
    let mut bad = Vec::new();
    for path in workflows_and_actions() {
        let text = std::fs::read_to_string(&path).expect("read");
        let file = path.display().to_string();
        for (n, line) in text.lines().enumerate() {
            if names_a_postgresql_major(line) {
                bad.push(format!(
                    "{file}:{}: hardcoded PostgreSQL major: {}",
                    n + 1,
                    line.trim()
                ));
            }
        }
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parses");
        let mut envs: Vec<(String, &serde_yaml::Value)> = Vec::new();
        if let Some(e) = doc.get("env") {
            envs.push(("workflow".into(), e));
        }
        let mut job_steps: Vec<(String, &serde_yaml::Value, Vec<&serde_yaml::Value>)> = Vec::new();
        if let Some(jobs) = doc.get("jobs").and_then(|j| j.as_mapping()) {
            for (name, job) in jobs {
                let name = name.as_str().unwrap_or("?").to_string();
                if let Some(e) = job.get("env") {
                    envs.push((format!("job {name}"), e));
                }
                let steps: Vec<&serde_yaml::Value> = job
                    .get("steps")
                    .and_then(|s| s.as_sequence())
                    .into_iter()
                    .flatten()
                    .collect();
                for s in &steps {
                    if let Some(e) = s.get("env") {
                        envs.push((format!("job {name} step"), e));
                    }
                }
                job_steps.push((name, job, steps));
            }
        }
        if let Some(steps) = doc
            .get("runs")
            .and_then(|r| r.get("steps"))
            .and_then(|s| s.as_sequence())
        {
            for s in steps {
                if let Some(e) = s.get("env") {
                    envs.push(("composite step".into(), e));
                }
            }
        }
        for (where_, env) in envs {
            if env.get("SKY_POSTGRES_BIN").is_some() {
                bad.push(format!(
                    "{file}: {where_} sets SKY_POSTGRES_BIN in `env:`; it comes from scripts/ci/resolve-postgres.sh"
                ));
            }
        }
        for (name, job, steps) in job_steps {
            let linux = job
                .get("runs-on")
                .and_then(|r| r.as_str())
                .is_some_and(|r| r.contains("ubuntu"));
            if !linux || job_is_disabled(job) {
                continue;
            }
            for (i, s) in steps.iter().enumerate() {
                let run = s.get("run").and_then(|r| r.as_str()).unwrap_or("");
                if needs_runner_postgres(run) && !steps[..i].iter().any(|p| resolves_postgres(p)) {
                    bad.push(format!(
                        "{file}: job {name} step {:?} needs a runner PostgreSQL but no earlier step \
                         runs scripts/ci/resolve-postgres.sh (or gate-setup with postgres: 'true')",
                        s.get("name").and_then(|n| n.as_str()).unwrap_or("?")
                    ));
                }
            }
        }
    }
    assert!(
        bad.is_empty(),
        "PostgreSQL pinned outside the resolver:\n  {}",
        bad.join("\n  ")
    );

    let resolver = std::fs::read_to_string(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../scripts/ci/resolve-postgres.sh"
    ))
    .expect("read resolve-postgres.sh");
    for must in [
        "SKY_POSTGRES_BIN=",
        "GITHUB_ENV",
        "::error::",
        "apt_install postgresql\n",
    ] {
        assert!(
            resolver.contains(must),
            "scripts/ci/resolve-postgres.sh lost `{}`: it must install the distribution's \
             unnumbered `postgresql`, fail with ::error:: on a missing binary, and export \
             SKY_POSTGRES_BIN to $GITHUB_ENV",
            must.trim()
        );
    }
}

/// The Linux release binaries link against a pinned glibc floor, and each
/// build leg proves it before anything is uploaded.
///
/// The legs run on the CURRENT Ubuntu runners so a breaking image change shows
/// up early. A binary linked normally needs the build host's glibc (24.04 gives
/// 2.39, which Debian 12, Ubuntu 22.04 and RHEL 9 cannot run), so the legs
/// build with `cargo zigbuild --target <triple>.$SKY_GLIBC_FLOOR`, and two
/// steps before `Package` check the result: the highest GLIBC_x.y symbol
/// version against the floor, and `--version` inside debian:12.
#[test]
fn linux_release_binaries_link_against_the_glibc_floor_and_prove_it() {
    let doc = read_workflow("release.yml");
    let floor = doc["env"]["SKY_GLIBC_FLOOR"]
        .as_str()
        .expect("release.yml declares env.SKY_GLIBC_FLOOR as a string");
    let is_version = floor
        .strip_prefix("2.")
        .is_some_and(|minor| !minor.is_empty() && minor.bytes().all(|b| b.is_ascii_digit()));
    assert!(
        is_version,
        "SKY_GLIBC_FLOOR must be a glibc version like 2.35, got {floor:?}"
    );

    let build = &doc["jobs"]["build"];
    assert!(
        !build.is_null() && !job_is_disabled(build),
        "release.yml has no enabled build job"
    );
    let legs = build["strategy"]["matrix"]["include"]
        .as_sequence()
        .expect("build.strategy.matrix.include");
    let linux: Vec<&serde_yaml::Value> = legs
        .iter()
        .filter(|l| {
            l["artifact"]
                .as_str()
                .is_some_and(|a| a.starts_with("sky-linux-"))
        })
        .collect();
    assert_eq!(
        linux.len(),
        2,
        "expected the sky-linux-x64 and sky-linux-arm64 legs"
    );
    for leg in &linux {
        let artifact = leg["artifact"].as_str().unwrap();
        let target = leg["rust-target"].as_str().unwrap_or("");
        assert!(
            target.ends_with("-unknown-linux-gnu"),
            "{artifact}: rust-target must be a *-unknown-linux-gnu triple (zigbuild appends the \
             floor), got {target:?}"
        );
        let os = leg["os"].as_str().unwrap_or("");
        assert!(
            os == "ubuntu-latest" || os == "ubuntu-24.04-arm",
            "{artifact}: the Linux legs run on the current runners (ubuntu-latest / \
             ubuntu-24.04-arm); the glibc floor comes from zigbuild, not an old image. Got {os:?}"
        );
    }

    let steps = build["steps"].as_sequence().expect("build steps");
    let run_of = |s: &serde_yaml::Value| s["run"].as_str().unwrap_or("").to_string();
    let idx = |pred: &dyn Fn(&serde_yaml::Value) -> bool, what: &str| {
        steps
            .iter()
            .position(|s| pred(s))
            .unwrap_or_else(|| panic!("release.yml build job has no {what} step"))
    };
    let compile = idx(
        &|s| {
            let r = run_of(s);
            r.contains("cargo zigbuild")
                && r.contains("--target \"$target\"")
                && r.contains("${{ matrix.rust-target }}.${SKY_GLIBC_FLOOR}")
        },
        "`cargo zigbuild --target <rust-target>.$SKY_GLIBC_FLOOR`",
    );
    let symbols = idx(
        &|s| {
            let r = run_of(s);
            r.contains("SKY_GLIBC_FLOOR")
                && r.contains("objdump -T")
                && r.contains("sky-ffi-inspect-")
        },
        "glibc symbol-version check (objdump -T against SKY_GLIBC_FLOOR, both binaries)",
    );
    let debian = idx(
        &|s| {
            let r = run_of(s);
            r.contains("docker run") && r.contains("debian:12") && r.contains("--version")
        },
        "`--version` inside debian:12",
    );
    let package = idx(&|s| s["name"].as_str() == Some("Package"), "Package");
    let upload = idx(
        &|s| {
            s["uses"]
                .as_str()
                .is_some_and(|u| u.starts_with("actions/upload-artifact@"))
        },
        "upload-artifact",
    );
    for (i, what) in [(symbols, "glibc symbol check"), (debian, "debian:12 run")] {
        let step = &steps[i];
        assert_eq!(
            step["if"].as_str(),
            Some("matrix.rust-target != ''"),
            "the {what} must run on exactly the Linux legs"
        );
        assert!(
            compile < i && i < package && i < upload,
            "the {what} must run after the build and BEFORE Package and upload-artifact, so a \
             binary that fails it never reaches the publish job"
        );
        assert!(
            step.get("continue-on-error").is_none(),
            "the {what} must fail the leg"
        );
    }
}

/// Every step of a parsed workflow (`jobs.*.steps`) or composite action
/// (`runs.steps`), with the name of the job it sits in.
fn all_steps(doc: &serde_yaml::Value) -> Vec<(String, &serde_yaml::Value)> {
    let mut out = Vec::new();
    if let Some(jobs) = doc.get("jobs").and_then(|j| j.as_mapping()) {
        for (k, job) in jobs {
            let name = k.as_str().unwrap_or("?").to_string();
            for s in job["steps"].as_sequence().into_iter().flatten() {
                out.push((name.clone(), s));
            }
        }
    }
    for s in doc["runs"]["steps"].as_sequence().into_iter().flatten() {
        out.push(("(composite)".to_string(), s));
    }
    out
}

/// What breaks the "only main saves a cache" rule in one parsed file.
fn cache_discipline_problems(file: &str, doc: &serde_yaml::Value) -> Vec<String> {
    let main_only = |cond: &str| cond.contains("github.ref == 'refs/heads/main'");
    let mut problems = Vec::new();
    for (job, s) in all_steps(doc) {
        let uses = s["uses"].as_str().unwrap_or("");
        let with = &s["with"];
        let at = format!("{file} `{job}`");
        if uses.starts_with("actions/cache@") {
            problems.push(format!(
                "{at}: `{uses}` saves on every ref; use actions/cache/restore and a \
                 main-only actions/cache/save"
            ));
        }
        if uses.starts_with("actions/cache/save@") && !s["if"].as_str().is_some_and(main_only) {
            problems.push(format!(
                "{at}: actions/cache/save without `if: ... github.ref == 'refs/heads/main'`"
            ));
        }
        if uses.starts_with("Swatinem/rust-cache@") {
            let save = match &with["save-if"] {
                serde_yaml::Value::String(v) => v.trim().to_string(),
                serde_yaml::Value::Bool(b) => b.to_string(),
                _ => String::new(),
            };
            if save != "false" && !main_only(&save) {
                problems.push(format!(
                    "{at}: rust-cache `save-if: {save:?}`; it must be 'false' or main-only \
                     (a missing save-if saves on every ref)"
                ));
            }
        }
        if uses.starts_with("actions/setup-go@") {
            let cache_off = matches!(&with["cache"], serde_yaml::Value::Bool(false))
                || with["cache"].as_str() == Some("false");
            if !cache_off {
                problems.push(format!(
                    "{at}: actions/setup-go without `cache: false` saves its own cache on \
                     every ref; restore the main-only Go family instead"
                ));
            }
        }
        if with["cache-to"]
            .as_str()
            .is_some_and(|c| c.contains("type=gha"))
        {
            problems.push(format!(
                "{at}: a docker `type=gha` cache-to saves on every ref"
            ));
        }
    }
    problems
}

/// Only `refs/heads/main` saves a cache. Every other ref restores.
///
/// # Why this exists
///
/// GitHub gives a repository 10 GB of Actions cache, evicted least recently
/// used. Every branch, tag and pull request saved its own entries: on
/// 2026-10-16 the repository held 31 entries (10.5 GB), 26 of them from one
/// branch, and main's entries were evicted. A branch can restore main's caches
/// but not another branch's, so every run started cold: rust-cache found
/// nothing, and the Go build caches were missing in rust-ci run 38764872817 and
/// release run 38764879819. With main as the only writer, each family has one
/// entry, the total stays far under the budget, and every ref restores it.
///
/// Parsed, not grepped: a comment cannot vouch for a step.
#[test]
fn caches_are_saved_on_main_only() {
    let mut problems = Vec::new();
    for path in workflows_and_actions() {
        let text = std::fs::read_to_string(&path).expect("read");
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parses");
        let file = path.file_name().unwrap().to_string_lossy().to_string();
        let file = if file.starts_with("action.") {
            path.parent()
                .and_then(|p| p.file_name())
                .map(|n| n.to_string_lossy().to_string())
                .unwrap_or(file)
        } else {
            file
        };
        problems.extend(cache_discipline_problems(&file, &doc));
    }
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

/// The check above goes red on each shape that saves a cache off main.
#[test]
fn the_cache_discipline_check_catches_a_branch_save() {
    let cases = [
        ("save-always cache", "jobs:\n  a:\n    steps:\n      - uses: actions/cache@v6\n        with:\n          path: x\n          key: k\n"),
        ("unguarded save", "jobs:\n  a:\n    steps:\n      - uses: actions/cache/save@v6\n        with:\n          path: x\n          key: k\n"),
        ("rust-cache default", "jobs:\n  a:\n    steps:\n      - uses: Swatinem/rust-cache@v2\n        with:\n          workspaces: rust\n"),
        ("rust-cache true", "jobs:\n  a:\n    steps:\n      - uses: Swatinem/rust-cache@v2\n        with:\n          save-if: 'true'\n"),
        ("setup-go cache", "jobs:\n  a:\n    steps:\n      - uses: actions/setup-go@v7\n        with:\n          go-version: '1.26'\n"),
        ("docker gha", "jobs:\n  a:\n    steps:\n      - uses: docker/build-push-action@v7\n        with:\n          cache-to: type=gha,mode=max\n"),
        ("composite save", "runs:\n  using: composite\n  steps:\n    - uses: actions/cache@v6\n      with:\n        path: x\n        key: k\n"),
    ];
    for (what, yaml) in cases {
        let doc: serde_yaml::Value = serde_yaml::from_str(yaml).expect(what);
        assert!(
            !cache_discipline_problems("t.yml", &doc).is_empty(),
            "the check missed: {what}"
        );
    }
    let ok = "jobs:\n  a:\n    steps:\n      - uses: actions/cache/restore@v6\n        with:\n          path: x\n          key: k\n      - uses: actions/cache/save@v6\n        if: always() && github.ref == 'refs/heads/main'\n        with:\n          path: x\n          key: k\n      - uses: Swatinem/rust-cache@v2\n        with:\n          save-if: ${{ github.ref == 'refs/heads/main' }}\n      - uses: actions/setup-go@v7\n        with:\n          cache: false\n";
    let doc: serde_yaml::Value = serde_yaml::from_str(ok).unwrap();
    assert_eq!(
        cache_discipline_problems("t.yml", &doc),
        Vec::<String>::new()
    );
}

/// Every `scripts/ci/go-test-shard.sh K/N <packages> -- <flags>` split, in
/// either workflow, runs all N shards exactly once, with the same packages and
/// flags, in jobs that gate the result (`ci-green` needs them in rust-ci.yml,
/// `release` needs them in release.yml).
///
/// The race run of the Go runtime tests is split by test across two jobs
/// (rust-ci `integration-postgres` / `-2`, release `gate-race-1` / `-2`). The
/// script derives each shard from one sorted list, so the shards are disjoint
/// and total once every K in 1..=N runs (go_test_shards_are_disjoint_and_total.rs
/// proves the partition). Deleting a job, or two jobs naming the same shard,
/// would leave half the tests unrun while every job reports green.
#[test]
fn go_test_shards_run_every_shard_once_in_a_gating_job() {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    let mut splits_seen = 0;
    for (file, fan_in) in [("rust-ci.yml", "ci-green"), ("release.yml", "release")] {
        let text = std::fs::read_to_string(root.join(".github/workflows").join(file))
            .expect("read workflow");
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parses");
        let jobs = doc.get("jobs").and_then(|j| j.as_mapping()).expect("jobs");
        let gating: Vec<String> = doc["jobs"][fan_in]["needs"]
            .as_sequence()
            .map(|s| {
                s.iter()
                    .filter_map(|v| v.as_str())
                    .map(str::to_string)
                    .collect()
            })
            .unwrap_or_default();
        // (packages and flags) -> [(k, n, job)]
        let mut splits: std::collections::BTreeMap<String, Vec<(u32, u32, String)>> =
            Default::default();
        for (name, job) in jobs {
            let Some(name) = name.as_str() else { continue };
            if job_is_disabled(job) {
                continue;
            }
            for step in job["steps"].as_sequence().into_iter().flatten() {
                let run = step["run"].as_str().unwrap_or("");
                for line in run.lines().map(str::trim).filter(|l| !l.starts_with('#')) {
                    let toks: Vec<&str> = line.split_whitespace().collect();
                    let Some(i) = toks.iter().position(|t| t.ends_with("go-test-shard.sh")) else {
                        continue;
                    };
                    let spec = toks.get(i + 1).copied().unwrap_or("");
                    let (k, n) = spec.split_once('/').unwrap_or_else(|| {
                        panic!("{file} `{name}`: bad shard `{spec}` in `{line}`")
                    });
                    let key = toks[i + 2..].join(" ");
                    splits.entry(key).or_default().push((
                        k.parse().expect("K"),
                        n.parse().expect("N"),
                        name.to_string(),
                    ));
                }
            }
        }
        assert!(
            !splits.is_empty(),
            "{file}: no go-test-shard.sh split found"
        );
        for (key, shards) in &splits {
            splits_seen += 1;
            let n = shards[0].1;
            assert!(
                shards.iter().all(|(_, m, _)| *m == n),
                "{file} `{key}`: shards disagree on N: {shards:?}"
            );
            let mut ks: Vec<u32> = shards.iter().map(|(k, _, _)| *k).collect();
            ks.sort();
            assert_eq!(
                ks,
                (1..=n).collect::<Vec<u32>>(),
                "{file} `{key}`: shards must be 1..={n}, each exactly once: {shards:?}"
            );
            let ungated: Vec<&String> = shards
                .iter()
                .map(|(_, _, j)| j)
                .filter(|j| !gating.contains(j))
                .collect();
            assert!(
                ungated.is_empty(),
                "{file} `{key}`: shard job(s) {ungated:?} are not in `{fan_in}: needs:`"
            );
        }
    }
    assert!(splits_seen >= 2);
}
