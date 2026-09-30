//! `testrunner` — `sky test` (Sky.Test) runner (doc 02, doc 10). Synthesises a
//! temporary entry module that imports the suite and calls
//! `Sky.Test.runMain Suite.tests`, builds + runs it through the shared
//! [`project`] driver, and propagates the exit code so CI sees failures
//! (`app/Main.hs:1413`). The synthesised entry is removed regardless of outcome.

use project::{
    assets_root_for, build_project_scoped, configured_bin_name, declared_module_name,
    module_name_from_path, project_dir_for, source_root_for_declared, AppScope, BuildOptions,
};
use std::path::Path;

/// A summary of a `sky test` run.
#[derive(Copy, Clone, PartialEq, Eq, Debug, Default)]
pub struct TestSummary {
    pub files_analyzed: usize,
}

/// Outcome of running a Sky.Test suite. `exit_code` mirrors the compiled test
/// binary's process exit (0 = all passed) so the CLI can propagate it.
#[derive(Clone, Debug, Default)]
pub struct TestRun {
    pub emitted: bool,
    pub build_ok: bool,
    /// Set when the binary was built and executed.
    pub exit_code: Option<i32>,
    /// A short human note when something upstream of running failed.
    pub note: String,
    /// The build's structured diagnostics (warnings, and the failure when the
    /// suite did not build) — what `sky test --format json` prints.
    pub diagnostics: Vec<project::diagnostics::Reported>,
}

/// How [`run_test_with`] runs the suite binary.
#[derive(Clone, Debug, Default)]
pub struct TestOptions {
    /// Ask the suite for its per-case JSON report at this path (`SKY_TEST_JSON`).
    pub json_report: Option<std::path::PathBuf>,
    /// Send the suite binary's stdout (the human `ok` / `FAIL` lines) to
    /// stderr, so the caller's stdout stays machine-readable.
    pub stdout_to_stderr: bool,
}

/// `sky test` exit status: every test passed.
pub const EXIT_PASSED: u8 = 0;
/// `sky test` exit status: the suite ran and at least one test failed (or the
/// test binary itself died while running: a panic, a signal, a Go fatal error).
pub const EXIT_TESTS_FAILED: u8 = 1;
/// `sky test` exit status: no test ran. The suite did not build (a compile
/// error, a `go build` failure, a suite module that did not resolve), or the
/// runner could not start the test binary.
pub const EXIT_NOT_RUN: u8 = 2;

impl TestRun {
    /// The process exit status `sky test` reports for this run — the contract
    /// in `docs/tooling/testing.md`: 0 all passed, 1 tests failed, 2 nothing ran.
    ///
    /// A build failure used to leave `exit_code` as `None`, which the CLI
    /// mapped to `ExitCode::FAILURE` (1), so CI could not tell "the tests
    /// failed" from "the suite never compiled". Any non-zero exit from the test
    /// binary is reported as 1 — a Go fatal error exits the binary with 2, and
    /// passing that through would claim the BUILD failed.
    pub fn exit_status(&self) -> u8 {
        match self.exit_code {
            Some(0) => EXIT_PASSED,
            Some(_) => EXIT_TESTS_FAILED,
            None => EXIT_NOT_RUN,
        }
    }
}

/// The name of the synthesised entry module + file (never a user's own module).
const ENTRY_MODULE: &str = "SkyTestEntry__";

/// Build + run a Sky.Test suite at `suite_path`. Reuses the same [`project`]
/// build driver as `sky build` (no parallel pipeline).
///
/// **Everything ephemeral lands in a private scratch dir under
/// `std::env::temp_dir()`** — NEVER the project's own tree:
///   * the synthesised `SkyTestEntry__.sky` (so the user's `src/` stays
///     pristine), and
///   * the build output `sky-out/` (so a 4-test suite build can never clobber
///     an example's committed oracle binary at `<project>/sky-out/app` — the
///     regression this design closes).
///
/// The real `project_dir` is still passed as `example_dir` so the suite's
/// sibling modules, its FFI surface, and go.mod version pins load from the
/// project; only the *output* + the synth entry move to scratch. The scratch
/// dir is removed on every exit path.
///
/// `_out_dir_name` is accepted for signature stability with the CLI but is
/// ignored: output always goes to the scratch `sky-out/`.
pub fn run_test(suite_path: &Path, out_dir_name: &str) -> std::io::Result<TestRun> {
    run_test_with(suite_path, out_dir_name, &TestOptions::default())
}

/// [`run_test`] with [`TestOptions`] (the `sky test --format json` path).
pub fn run_test_with(
    suite_path: &Path,
    _out_dir_name: &str,
    topts: &TestOptions,
) -> std::io::Result<TestRun> {
    let mut run = TestRun::default();

    // `assets_root_for` (not `repo_root_for`) so `sky test` works in a standalone
    // `sky init` project too — it extracts the embedded stdlib + runtime when run
    // outside the compiler repo tree, exactly like `build`/`run`/`check`.
    let Some(repo_root) = assets_root_for(suite_path) else {
        run.note = "could not locate the Sky stdlib + runtime (embedded extraction failed)".into();
        return Ok(run);
    };
    let project_dir = project_dir_for(suite_path);

    // The suite's module identity is the name it DECLARES, not the one its path
    // suggests. The loader registers every module under its `module … exposing`
    // header; a path-derived guess that disagrees does NOT fail the build —
    // `classify_import` treats an unknown module as a Go FFI package, the
    // `Suite.tests` reference lowers to `nil`, and the run reports
    // "0 passed, 0 failed (0 total)" and exits 0. Deriving from the path is only
    // a fallback for a headerless file.
    let declared = declared_module_name(suite_path);
    let Some(module) = declared
        .clone()
        .or_else(|| module_name_from_path(&project_dir, &["src", "tests"], suite_path))
    else {
        run.note = format!(
            "{} declares no `module …` header and its module name cannot be \
             derived from its path",
            suite_path.display()
        );
        return Ok(run);
    };

    // A per-invocation scratch dir: <tmp>/sky-test-<pid>-<nanos>/. Uniqueness
    // (pid + monotonic nanos) keeps concurrent `sky test` runs from colliding.
    let scratch = scratch_dir();
    std::fs::create_dir_all(&scratch)?;

    // Synthesise the entry INTO the scratch dir (flat), then feed the scratch
    // dir to the build as an extra source root. `collect_sky` prunes any
    // `sky-out/` beneath it, so the scratch's own build output is never
    // re-scanned as source.
    let entry_file = scratch.join(format!("{ENTRY_MODULE}.sky"));
    let entry_body = format!(
        "module {ENTRY_MODULE} exposing (main)\n\n\
         import Sky.Test as Test\n\
         import {module} as Suite\n\n\
         main =\n    Test.runMain Suite.tests\n"
    );
    std::fs::write(&entry_file, entry_body)?;

    let out_dir = scratch.join("sky-out");
    let opts = BuildOptions {
        repo_root,
        example_dir: project_dir.clone(),
        out_dir_name: "sky-out".to_string(),
        out_dir_abs: Some(out_dir.clone()),
        run: false,
        stdin: None,
        entry_module: None,
        progress: false,
        embed_bundle: None,
        wasm: false,
    };
    // Extra source roots: the project's `tests/` tree (carries the suite when it
    // lives under tests/), the root the suite's DECLARED name is relative to (so
    // `tests/Std/ThingTest.sky` declaring `Std.ThingTest` loads from `tests/`
    // even when that is the project root itself), and the scratch dir (carries
    // the synth entry).
    let mut extra = vec![project_dir.join("tests")];
    // Canonicalised, like `project_dir`: the suite path is usually RELATIVE
    // (`tests/FooTest.sky`), so without this `tests` and `<abs>/tests` compared
    // unequal, the same tree was loaded twice, and every diagnostic in it was
    // printed twice.
    if let Some(root) = declared
        .as_deref()
        .and_then(|d| source_root_for_declared(suite_path, d))
        .map(|r| r.canonicalize().unwrap_or(r))
    {
        if !extra.contains(&root) {
            extra.push(root);
        }
    }
    extra.push(scratch.clone());
    // Only the suite and the modules it imports are built: a type error in
    // another suite under `tests/` (or in an app module the suite does not
    // import) must not stop this one.
    let report = build_project_scoped(&opts, &extra, Some(ENTRY_MODULE), AppScope::EntryClosure);

    run.emitted = report.emitted;
    run.build_ok = report.go_build_ok;
    run.diagnostics = report.diagnostics();

    // The suite reference must have resolved to a real Sky module. If it fell
    // through to the FFI-package path the lowerer emits `nil` for `Suite.tests`
    // and the run reports a cheerful "0 passed" — the exact failure this guard
    // exists to make impossible. `sky build`/`sky run` print these warnings;
    // `sky test` used to drop them, which is why nothing caught it.
    // Since v0.25.19 lowering also REFUSES that reference (a hard error naming
    // `Suite.tests`, never a `nil`), so the build no longer emits; the guard
    // matches either signal so the suite-specific note below still wins.
    let foreign = report
        .warnings
        .iter()
        .any(|w| w.contains("foreign ref") && w.contains(&format!("{module}.")))
        || (!report.emitted && report.note.contains(&format!("`{module}.tests`")));
    if foreign {
        run.emitted = false;
        run.build_ok = false;
        run.note = format!(
            "suite module `{module}` did not resolve — it was treated as a Go FFI \
             package, so its `tests` would run as an EMPTY list. Check that {} \
             declares `module {module}` and sits under a source root.",
            suite_path.display()
        );
        run.diagnostics
            .retain(|d| d.severity != project::diagnostics::Severity::Error);
        run.diagnostics.push(project::diagnostics::Reported::plain(
            project::diagnostics::Severity::Error,
            project::diagnostics::Origin::Sky,
            run.note.clone(),
        ));
        let _ = std::fs::remove_dir_all(&scratch);
        return Ok(run);
    }

    if !report.emitted {
        run.note = report.note;
    } else if !report.go_build_ok {
        run.note = report
            .go_build_stderr
            .lines()
            .find(|l| l.contains("error") || l.contains(".go:"))
            .unwrap_or("go build failed")
            .trim()
            .to_string();
    } else {
        // Run the compiled test binary with inherited stdio; propagate exit code.
        // The binary was emitted under the PROJECT's configured `bin` name (the
        // build reads `project_dir`'s sky.toml), NOT the scratch's — so derive the
        // name the same way, or a project with `bin = "myapp"` would look for a
        // non-existent `sky-out/app`. cwd is the project dir so a suite's relative
        // fixtures / data files resolve where the author expects (#5).
        let bin_abs = out_dir.join(configured_bin_name(&project_dir));
        let mut cmd = std::process::Command::new(&bin_abs);
        cmd.current_dir(&project_dir);
        if let Some(p) = &topts.json_report {
            cmd.env("SKY_TEST_JSON", p);
        }
        if topts.stdout_to_stderr {
            cmd.stdout(std::process::Stdio::from(std::io::stderr()));
        }

        // Test-mode activation (opt-in): a project declares it by committing a
        // `.env.test`. When present, run the suite in TEST MODE — set
        // SKY_TEST_MODE (the runtime then serves the offline HTTP mock and fails
        // closed on any unmocked outbound request; determinism stays opt-in via
        // SKY_TEST_SEED / SKY_TEST_CLOCK_MS) — and load `.env.test` then
        // `.env.test.local` (override) into the child env. `.env.test` is the
        // committed non-secret config + mock toggles; `.env.test.local` the
        // gitignored sandbox creds for the opt-in contract-drift tier. A project
        // with no `.env.test` runs exactly as before. The runtime's default mocks
        // dir (`tests/mocks/`, cwd-relative) resolves because cwd is the project.
        if project_dir.join(".env.test").is_file() {
            cmd.env("SKY_TEST_MODE", "1");
            // A per-run log-capture file so a scenario can assert on what the app
            // logged (read it back with File.read on the same env var). Removed
            // with the scratch dir at the end of the run.
            let log_capture = scratch.join("captured-logs.txt");
            cmd.env("SKY_TEST_LOG_CAPTURE", &log_capture);
            let mut has_dsn = dsn_is_given(std::env::var("DATABASE_URL").ok().as_deref());
            for f in [".env.test", ".env.test.local"] {
                if let Ok(contents) = std::fs::read_to_string(project_dir.join(f)) {
                    for (k, v) in parse_dotenv(&contents) {
                        if k == "DATABASE_URL" && dsn_is_given(Some(&v)) {
                            has_dsn = true;
                        }
                        cmd.env(k, v);
                    }
                }
            }
            // EPHEMERAL DB (auto-testing phase 3b): if the project declares a
            // database but no DSN is provided, give it an OFFLINE database in the
            // run's scratch dir, thrown away with the scratch dir at the end. The
            // engine decides how (see `project::offline_db_plan`): a Postgres app
            // gets a throwaway EMBEDDED cluster (created + migrated by the app's
            // own schema setup); a SQLite app is already offline and just has its
            // path redirected to a scratch file — forcing embedded Postgres onto a
            // SQLite app is a conflict the runtime rejects, so branching on the
            // engine is load-bearing. Skipped when a DSN IS given (the test
            // targets that DB). The temp data dir is allowed only because
            // SKY_TEST_MODE is set (see rejectTempDataDir); a prod --embed app is
            // unaffected.
            if !has_dsn {
                match project::offline_db_plan(&project_dir) {
                    project::OfflineDbPlan::Postgres => {
                        cmd.env("SKY_EMBED_POSTGRES", "1");
                        cmd.env("SKY_DATA_DIR", scratch.join("pgdata"));
                    }
                    project::OfflineDbPlan::Sqlite { db_path_env } => {
                        cmd.env(db_path_env, scratch.join("test.db"));
                    }
                    project::OfflineDbPlan::None => {}
                }
            }
        }

        match cmd.status() {
            Ok(status) => run.exit_code = Some(status.code().unwrap_or(1)),
            Err(e) => run.note = format!("run failed: {e}"),
        }
    }

    // Always remove the whole scratch dir (synth entry + build output).
    let _ = std::fs::remove_dir_all(&scratch);
    Ok(run)
}

/// Whether a `DATABASE_URL` value names a database. An EMPTY (or blank) value
/// is no DSN: the docs promise a project "given no DSN" its throwaway database,
/// and `DATABASE_URL=` is the usual way to clear one (a CI job, a `.env.test`
/// that blanks a developer's `.env`). Before this an empty value counted as a
/// DSN, so `sky test` started no database and the suite ran against nothing.
pub fn dsn_is_given(value: Option<&str>) -> bool {
    value.is_some_and(|v| !v.trim().is_empty())
}

/// Parse a minimal `.env` file for test-mode activation: `KEY=VALUE` per line,
/// `#` comments and blank lines skipped, an optional `export ` prefix stripped,
/// and surrounding single/double quotes removed from the value. Not a full dotenv
/// implementation (no interpolation, no multiline) — enough for test config and
/// mock toggles.
fn parse_dotenv(contents: &str) -> Vec<(String, String)> {
    let mut out = Vec::new();
    for line in contents.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let line = line.strip_prefix("export ").unwrap_or(line);
        let Some((k, v)) = line.split_once('=') else {
            continue;
        };
        let k = k.trim();
        if k.is_empty() {
            continue;
        }
        let v = v.trim();
        let v = v
            .strip_prefix('"')
            .and_then(|s| s.strip_suffix('"'))
            .or_else(|| v.strip_prefix('\'').and_then(|s| s.strip_suffix('\'')))
            .unwrap_or(v);
        out.push((k.to_string(), v.to_string()));
    }
    out
}

/// A unique scratch directory under the OS temp dir for one `sky test` run.
fn scratch_dir() -> std::path::PathBuf {
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    std::env::temp_dir().join(format!("sky-test-{}-{}", std::process::id(), nanos))
}

/// M0 placeholder retained for the crate-DAG smoke test.
pub fn run_stub(sources: &[&str]) -> TestSummary {
    let p = project::Project::new();
    for (i, src) in sources.iter().enumerate() {
        let _ = p.analyze(i as u32, src);
    }
    TestSummary {
        files_analyzed: sources.len(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// An empty or blank `DATABASE_URL` is no DSN: `sky test` still starts the
    /// throwaway database (tests/sky_test_empty_dsn_flow.rs runs it end to end).
    #[test]
    fn an_empty_dsn_is_no_dsn() {
        assert!(!dsn_is_given(None));
        assert!(!dsn_is_given(Some("")));
        assert!(!dsn_is_given(Some("  ")));
        assert!(dsn_is_given(Some("postgres://u@h/db")));
    }

    #[test]
    fn runs_over_the_project_driver() {
        let s = run_stub(&["a\n", "b\nc\n"]);
        assert_eq!(s.files_analyzed, 2);
    }

    /// The exit-status contract: 0 pass, 1 test failure, 2 nothing ran. A build
    /// failure (`exit_code == None`) used to surface as 1.
    #[test]
    fn exit_status_distinguishes_build_failure_from_test_failure() {
        let run = |code: Option<i32>| TestRun {
            exit_code: code,
            ..TestRun::default()
        };
        assert_eq!(run(Some(0)).exit_status(), 0, "all tests passed");
        assert_eq!(run(Some(1)).exit_status(), 1, "a test failed");
        assert_eq!(
            run(Some(2)).exit_status(),
            1,
            "the test binary died with Go's fatal exit 2: that is a failed run, not a build failure"
        );
        assert_eq!(run(Some(137)).exit_status(), 1, "killed by a signal");
        assert_eq!(
            run(None).exit_status(),
            2,
            "the suite never built, so no test ran"
        );
    }

    #[test]
    fn parse_dotenv_handles_comments_quotes_and_export() {
        let src = "# a comment\n\nexport DATABASE_URL=postgres://x:y@localhost:5433/db\nDS_STRIPE_WEBHOOK_SECRET=\"whsec_test\"\nQUOTED='single'\n  SPACED = val \n=novalue\nBAD_LINE_NO_EQUALS\n";
        let got = parse_dotenv(src);
        assert_eq!(
            got,
            vec![
                (
                    "DATABASE_URL".to_string(),
                    "postgres://x:y@localhost:5433/db".to_string()
                ),
                (
                    "DS_STRIPE_WEBHOOK_SECRET".to_string(),
                    "whsec_test".to_string()
                ),
                ("QUOTED".to_string(), "single".to_string()),
                ("SPACED".to_string(), "val".to_string()),
            ]
        );
    }

    /// #5: a project with a custom `bin` name must still run its tests. Before the
    /// fix, `run_test` looked for `sky-out/app` while the build emitted the
    /// binary under the configured `bin` (e.g. `custombin`), so `sky test`
    /// reported "run failed: … No such file". This builds a real project with
    /// `bin = "custombin"` + a passing suite and asserts a clean exit 0.
    #[test]
    fn respects_custom_bin_name() {
        let dir = std::env::temp_dir().join(format!(
            "sky-testrunner-custombin-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos())
                .unwrap_or(0)
        ));
        let tests_dir = dir.join("tests");
        std::fs::create_dir_all(&tests_dir).unwrap();
        std::fs::write(
            dir.join("sky.toml"),
            "name = \"custombintest\"\nversion = \"0.1.0\"\n\
             entry = \"src/Main.sky\"\nbin = \"custombin\"\n\n[source]\nroot = \"src\"\n",
        )
        .unwrap();
        let suite = tests_dir.join("SmokeTest.sky");
        std::fs::write(
            &suite,
            "module SmokeTest exposing (tests)\n\n\
             import Sky.Core.Prelude exposing (..)\n\
             import Sky.Test as Test exposing (Test)\n\n\
             tests : List Test\n\
             tests =\n    [ Test.suite \"smoke\" [ Test.test \"ok\" (\\_ -> Test.equal 2 (1 + 1)) ] ]\n",
        )
        .unwrap();

        let run = run_test(&suite, "sky-out").expect("run_test should not error");
        assert!(
            run.emitted && run.build_ok,
            "custom-bin project must build; note: {}",
            run.note
        );
        assert_eq!(
            run.exit_code,
            Some(0),
            "custom-bin test binary must run to a clean exit; note: {}",
            run.note
        );
        let _ = std::fs::remove_dir_all(&dir);
    }
}
