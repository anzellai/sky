//! `testrunner` — `sky test` (Sky.Test) runner (doc 02, doc 10). Drives the
//! `project` build then executes the compiled test binary.
//!
//! M0 stub: the runner type exists over the `project` driver; real Sky.Test
//! discovery + execution land in M5.

use project::Project;

/// A summary of a `sky test` run. Real fields (pass/fail counts, per-assertion
/// results) grow in M5.
#[derive(Copy, Clone, PartialEq, Eq, Debug, Default)]
pub struct TestSummary {
    pub files_analyzed: usize,
}

/// M0 placeholder: proves the `testrunner` → `project` edge by driving the
/// shared query db over a trivial input.
pub fn run_stub(sources: &[&str]) -> TestSummary {
    let mut p = Project::new();
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
