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
    let dir = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../../.github/workflows"));
    let mut out: Vec<PathBuf> = std::fs::read_dir(&dir)
        .unwrap_or_else(|e| panic!("cannot read {}: {e}", dir.display()))
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| matches!(p.extension().and_then(|x| x.to_str()), Some("yml" | "yaml")))
        .collect();
    out.sort();
    assert!(!out.is_empty(), "no workflows found — the glob is wrong, not the repo");
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
        let jobs = doc.get("jobs").unwrap_or_else(|| panic!("{}: no `jobs`", path.display()));
        let map = jobs
            .as_mapping()
            .unwrap_or_else(|| panic!("{}: `jobs` is not a mapping", path.display()));
        assert!(!map.is_empty(), "{}: `jobs` is empty", path.display());
        for (name, job) in map {
            let name = name.as_str().unwrap_or("<non-string>");
            // A job either runs steps or delegates to a reusable workflow.
            let has_steps = job.get("steps").and_then(|s| s.as_sequence()).is_some_and(|s| !s.is_empty());
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
    let jobs = doc.get("jobs").and_then(|j| j.as_mapping()).expect("`jobs` mapping");

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
        serde_yaml::Value::Sequence(s) => {
            s.iter().filter_map(|v| v.as_str()).map(str::to_string).collect()
        }
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
const JOB_LEVEL_KEYS: [&str; 5] =
    ["env", "if", "runs-on", "timeout-minutes", "continue-on-error"];

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
            while i < bytes.len() && (bytes[i].is_ascii_alphanumeric() || bytes[i] == '_' || bytes[i] == '-') {
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
        let Some(jobs) = doc.get("jobs").and_then(|j| j.as_mapping()) else { continue };
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
    assert_eq!(found, vec!["runner".to_string()], "the check no longer sees the original defect");

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
    let jobs = doc.get("jobs").and_then(|j| j.as_mapping()).expect("`jobs` mapping");

    // Only jobs the required check fans in count: a step in a job `ci-green`
    // does not need can go red while the merge proceeds.
    let fanned: Vec<String> = jobs
        .get(serde_yaml::Value::from("ci-green"))
        .and_then(|g| g.get("needs"))
        .and_then(|n| n.as_sequence())
        .map(|s| s.iter().filter_map(|v| v.as_str()).map(str::to_string).collect())
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
/// The `gate` job in `release.yml` — the job `release: needs: [build, gate]`
/// blocks publication on — has `run:` bodies that invoke, at minimum: the T2
/// tier, the T3 tier, the falsifier verification, and a full `example-sweep.sh`
/// that is NOT `--build-only` (it must RUN each example, not just compile it).
/// T1 and T4 already had explicit tests / were present; T2/T3/falsifiers/sweep
/// are the tiers that were nightly-only and are the point of §0.2.1.
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

    let gate = jobs
        .get(serde_yaml::Value::from("gate"))
        .expect("release.yml has a `gate` job");
    let steps = gate
        .get("steps")
        .and_then(|s| s.as_sequence())
        .expect("`gate` job has steps");

    // EXECUTABLE content only — `run:` bodies, never step names or comments.
    let mut haystack = String::new();
    for step in steps {
        if let Some(run) = step.get("run").and_then(|r| r.as_str()) {
            haystack.push_str(run);
            haystack.push('\n');
        }
    }
    assert!(
        haystack.contains("xtask"),
        "no `xtask` invocation found in any `gate` step `run:` body — the parse \
         is broken, not the repo"
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
        "release.yml's `gate` job does NOT run the full tier suite. CLAUDE.md \
         §0.2.1 (INVIOLABLE) requires every tier at a release — nothing deferred \
         to the nightly, which is exactly the gap that let the v0.21.0 \
         `record_update` codegen regression ship. Missing:\n  {}\n\n\
         Add each to a `run:` step of the `gate` job (T3 also needs the \
         `services: postgres` container). Mirror nightly-sweep.yml's invocations.",
        problems.join("\n  ")
    );
}

/// In `release.yml`'s `gate` job, the step that installs the compiler
/// (`scripts/build.sh` → `sky-out/sky`) MUST run BEFORE the codegen build+run
/// step (`xtask build-run`, whose `live`-shape examples spawn `sky build`).
///
/// The v0.23.0 release publish failed at exactly this point: `build-run --all`
/// ran first, so every live example died with `sky build spawn: No such file or
/// directory (os error 2)` while four platform binaries had already built —
/// publication blocked on step order, not a defect. The fix was a hand-edit to
/// the order; this test locks it, because the other checks in this file are
/// presence-only and both steps WERE present — just in the wrong order.
#[test]
fn release_gate_installs_the_compiler_before_build_run() {
    let path = workflows()
        .into_iter()
        .find(|p| p.file_name().and_then(|n| n.to_str()) == Some("release.yml"))
        .expect("release.yml must exist");
    let text = std::fs::read_to_string(&path).expect("read release.yml");
    let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("release.yml is parseable YAML");
    let gate = doc
        .get("jobs")
        .and_then(|j| j.get("gate"))
        .and_then(|g| g.get("steps"))
        .and_then(|s| s.as_sequence())
        .expect("release.yml has a `gate` job with steps");

    let mut build_sh_idx: Option<usize> = None;
    let mut build_run_idx: Option<usize> = None;
    for (i, step) in gate.iter().enumerate() {
        let run = step.get("run").and_then(|r| r.as_str()).unwrap_or("");
        if run.contains("build.sh") {
            build_sh_idx.get_or_insert(i);
        }
        if run.contains("build-run") {
            build_run_idx.get_or_insert(i);
        }
    }
    let bsh = build_sh_idx
        .expect("release.yml `gate` must install the compiler via scripts/build.sh before its gates run");
    let brun = build_run_idx.expect("release.yml `gate` must run `xtask build-run`");
    assert!(
        bsh < brun,
        "release.yml `gate` runs `build-run` (step {brun}) BEFORE it installs the compiler via \
         scripts/build.sh (step {bsh}). build-run's live-shape examples spawn `sky build`, which \
         needs sky-out/sky to exist — the v0.23.0 publish failed with `sky build spawn: No such \
         file or directory` for exactly this reason. Move the build.sh install step ahead of it."
    );
}
