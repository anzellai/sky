//! `testrunner` — `sky test` (Sky.Test) runner (doc 02, doc 10). Synthesises a
//! temporary entry module that imports the suite and calls
//! `Sky.Test.runMain Suite.tests`, builds + runs it through the shared
//! [`project`] driver, and propagates the exit code so CI sees failures
//! (`app/Main.hs:1413`). The synthesised entry is removed regardless of outcome.

use project::{build_project, module_name_from_path, project_dir_for, repo_root_for, run_app, BuildOptions};
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

/// Build + run a Sky.Test suite at `suite_path`, writing build output under
/// `<project>/<out_dir_name>`. Reuses the same [`project`] build driver as
/// `sky build` (no parallel pipeline). The synthesised `SkyTestEntry__.sky`
/// under `src/` is deleted on every exit path.
pub fn run_test(suite_path: &Path, out_dir_name: &str) -> std::io::Result<TestRun> {
    let mut run = TestRun::default();

    let Some(repo_root) = repo_root_for(suite_path) else {
        run.note = "could not locate compiler repo root (sky-stdlib + runtime-go)".into();
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

    // Synthesise the entry into src/ (the module-graph root the build scans).
    let src_dir = project_dir.join("src");
    std::fs::create_dir_all(&src_dir)?;
    let entry_file = src_dir.join(format!("{ENTRY_MODULE}.sky"));
    let entry_body = format!(
        "module {ENTRY_MODULE} exposing (main)\n\n\
         import Sky.Test as Test\n\
         import {module} as Suite\n\n\
         main =\n    Test.runMain Suite.tests\n"
    );
    std::fs::write(&entry_file, entry_body)?;

    let opts = BuildOptions {
        repo_root,
        example_dir: project_dir.clone(),
        out_dir_name: out_dir_name.to_string(),
        run: false,
        stdin: None,
    };
    // tests/ carries the suite module; load it alongside src/.
    let extra = vec![project_dir.join("tests")];
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
        let out_dir = project_dir.join(out_dir_name);
        match run_app(&out_dir, &[]) {
            Ok(status) => run.exit_code = Some(status.code().unwrap_or(1)),
            Err(e) => run.note = format!("run failed: {e}"),
        }
    }

    // Always remove the synthesised entry (closes the "left behind on a build
    // exception" footgun — app/Main.hs:1463).
    let _ = std::fs::remove_file(&entry_file);
    Ok(run)
}

/// M0 placeholder retained for the crate-DAG smoke test.
pub fn run_stub(sources: &[&str]) -> TestSummary {
    let mut p = project::Project::new();
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
}
