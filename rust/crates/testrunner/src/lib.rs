//! `testrunner` — `sky test` (Sky.Test) runner (doc 02, doc 10). Synthesises a
//! temporary entry module that imports the suite and calls
//! `Sky.Test.runMain Suite.tests`, builds + runs it through the shared
//! [`project`] driver, and propagates the exit code so CI sees failures
//! (`app/Main.hs:1413`). The synthesised entry is removed regardless of outcome.

use project::{
    assets_root_for, build_project, configured_bin_name, module_name_from_path, project_dir_for,
    BuildOptions,
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
pub fn run_test(suite_path: &Path, _out_dir_name: &str) -> std::io::Result<TestRun> {
    let mut run = TestRun::default();

    // `assets_root_for` (not `repo_root_for`) so `sky test` works in a standalone
    // `sky init` project too — it extracts the embedded stdlib + runtime when run
    // outside the compiler repo tree, exactly like `build`/`run`/`check`.
    let Some(repo_root) = assets_root_for(suite_path) else {
        run.note = "could not locate the Sky stdlib + runtime (embedded extraction failed)".into();
        return Ok(run);
    };
    let project_dir = project_dir_for(suite_path);

    let Some(module) = module_name_from_path(&project_dir, &["src", "tests"], suite_path) else {
        run.note = format!(
            "{} must live under src/ or tests/ so its module name can be derived",
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
    };
    // Extra source roots: the project's `tests/` tree (carries the suite when it
    // lives under tests/) and the scratch dir (carries the synth entry).
    let extra = vec![project_dir.join("tests"), scratch.clone()];
    let report = build_project(&opts, &extra, Some(ENTRY_MODULE));

    run.emitted = report.emitted;
    run.build_ok = report.go_build_ok;

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
        match cmd.status() {
            Ok(status) => run.exit_code = Some(status.code().unwrap_or(1)),
            Err(e) => run.note = format!("run failed: {e}"),
        }
    }

    // Always remove the whole scratch dir (synth entry + build output).
    let _ = std::fs::remove_dir_all(&scratch);
    Ok(run)
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

    #[test]
    fn runs_over_the_project_driver() {
        let s = run_stub(&["a\n", "b\nc\n"]);
        assert_eq!(s.files_analyzed, 2);
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
