//! Examples that NO CI job may build, train, evaluate or run.
//!
//! USER RULE (anzellai/sky#299, set by the user on 2026-10-27):
//! `examples/76-ai-shell-command` is EXCLUDED from CI in general. Its `train`
//! command fine-tunes a model for minutes to hours and its `eval` drives a
//! local model engine, which is far too long for CI. So no workflow, gate or
//! script may build, train, evaluate or run it, and no census counts it.
//!
//! Every gate that enumerates `examples/` skips the names below through
//! [`is_ci_excluded`], each site with a comment that cites this rule. Shell
//! scripts and the crates that cannot depend on `xtask` (`ty`, `project`) name
//! the example literally, with the same comment.
//! `tests/ci_excluded_examples.rs` fails the build when a workflow names an
//! excluded example, or when an enumerator of `examples/` does not skip it.
//!
//! Do not add an example here to make CI faster. An entry needs the same kind
//! of explicit user rule, cited in the comment above.

use std::path::Path;

/// The excluded examples, by directory name under `examples/`.
pub const CI_EXCLUDED_EXAMPLES: &[&str] = &["76-ai-shell-command"];

/// Is `name` (a directory name directly under `examples/`) excluded from CI?
pub fn is_ci_excluded(name: &str) -> bool {
    CI_EXCLUDED_EXAMPLES.contains(&name)
}

/// Is `path` inside an excluded example? Accepts a repo-relative path
/// (`examples/76-ai-shell-command/src/Main.sky`) or an absolute one: any
/// `examples/<excluded>` pair of components matches.
pub fn is_ci_excluded_path(path: &Path) -> bool {
    let parts: Vec<String> = path
        .components()
        .map(|c| c.as_os_str().to_string_lossy().into_owned())
        .collect();
    parts
        .windows(2)
        .any(|w| w[0] == "examples" && is_ci_excluded(&w[1]))
}

/// The same test on a repo-relative string with `/` separators.
pub fn is_ci_excluded_rel(rel: &str) -> bool {
    is_ci_excluded_path(Path::new(rel))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_shell_command_example_is_excluded() {
        assert!(is_ci_excluded("76-ai-shell-command"));
        assert!(!is_ci_excluded("75-ai-everywhere"));
        assert!(is_ci_excluded_rel(
            "examples/76-ai-shell-command/src/Main.sky"
        ));
        assert!(is_ci_excluded_path(Path::new(
            "/tmp/x/examples/76-ai-shell-command"
        )));
        assert!(!is_ci_excluded_rel(
            "examples/75-ai-everywhere/src/Main.sky"
        ));
        assert!(!is_ci_excluded_rel("docs/76-ai-shell-command.md"));
    }
}
