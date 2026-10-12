//! The project's AI pipeline entry: `src/Ai.sky`.
//!
//! A project declares its AI lifecycle as a `Std.Ai.Pipeline.Pipeline` value
//! named `pipeline` in the module `Ai` at the root of its source tree, the way
//! it declares `main` in `Main` (docs/design/std-ai-lifecycle.md §8.9). The
//! project-mode `sky ai` verbs (`eval`, `data freeze <name>`, `run`, a
//! pipeline slot's `start`, `promote`) build a program whose `main` is
//! `Std.Ai.Pipeline.commandLine Ai.pipeline` and run it.
//!
//! [`discover`] finds the entry and checks its shape before anything is built,
//! so a mistake is one line naming the fix rather than a type error from a
//! synthesised module.

use std::path::{Path, PathBuf};

/// The module that holds the pipeline.
pub const AI_MODULE: &str = "Ai";

/// The value the module must expose.
pub const AI_VALUE: &str = "pipeline";

/// The name of the synthesised entry module (never a user's own module).
pub const AI_ENTRY_MODULE: &str = "SkyAiEntry__";

/// A project's pipeline entry.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AiEntry {
    /// `<source root>/Ai.sky`.
    pub file: PathBuf,
    /// The project's source root.
    pub source_root: PathBuf,
}

/// The project's pipeline entry: `Ok(None)` when the project has no
/// `<source root>/Ai.sky`, an error naming the fix when the file does not
/// declare `module Ai` or does not expose `pipeline`.
pub fn discover(project_dir: &Path) -> Result<Option<AiEntry>, String> {
    let source_root = project_dir.join(crate::build::configured_source_root(project_dir));
    let file = source_root.join(format!("{AI_MODULE}.sky"));
    if !file.is_file() {
        return Ok(None);
    }
    let src = std::fs::read_to_string(&file).map_err(|e| format!("{}: {e}", file.display()))?;
    let shown = file
        .strip_prefix(project_dir)
        .unwrap_or(&file)
        .display()
        .to_string();
    check_header(&src).map_err(|why| format!("{shown}: {why}"))?;
    Ok(Some(AiEntry { file, source_root }))
}

/// Whether a source declares `module Ai` and exposes `pipeline` (by name or
/// with `(..)`).
fn check_header(src: &str) -> Result<(), String> {
    let parse = syntax::parse(src, base::FileId(0));
    let name = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text());
    match name.as_deref() {
        Some(AI_MODULE) => {}
        Some(other) => {
            return Err(format!(
                "declares `module {other}`; the AI pipeline module is `module {AI_MODULE} exposing ({AI_VALUE})`"
            ))
        }
        None => {
            return Err(format!(
                "has no module header; write `module {AI_MODULE} exposing ({AI_VALUE})` and define `{AI_VALUE} : Std.Ai.Pipeline.Pipeline`"
            ))
        }
    }
    let exposing = header_exposing(src).unwrap_or_default();
    let words: Vec<String> = exposing
        .replace(['(', ')', ','], " ")
        .split_whitespace()
        .map(str::to_string)
        .collect();
    if words.iter().any(|w| w == ".." || w == AI_VALUE) {
        Ok(())
    } else {
        Err(format!(
            "`module {AI_MODULE}` does not expose `{AI_VALUE}`; write `module {AI_MODULE} exposing ({AI_VALUE})` and define `{AI_VALUE} : Std.Ai.Pipeline.Pipeline`"
        ))
    }
}

/// The text of the module header's exposing list, from `exposing` to its
/// closing parenthesis, with `--` comments removed.
fn header_exposing(src: &str) -> Option<String> {
    let text: String = src
        .lines()
        .map(|l| l.split("--").next().unwrap_or(""))
        .collect::<Vec<_>>()
        .join("\n");
    let start = text.find("module")?;
    let after = &text[start..];
    let at = after.find("exposing")?;
    let rest = &after[at + "exposing".len()..];
    let open = rest.find('(')?;
    let mut depth = 0usize;
    for (i, c) in rest[open..].char_indices() {
        match c {
            '(' => depth += 1,
            ')' => {
                depth -= 1;
                if depth == 0 {
                    return Some(rest[open..open + i + 1].to_string());
                }
            }
            _ => {}
        }
    }
    None
}

/// The synthesised entry module's source.
pub fn entry_source() -> String {
    entry_source_with(None)
}

/// The synthesised entry, applying the app's `Sky.Config` `config` when it
/// comes from `config_module` (`config = <module>.config`), so the pipeline
/// reads the same database and settings the app does.
pub fn entry_source_with(config_module: Option<&str>) -> String {
    let (import, config) = match config_module {
        Some(m) if m == AI_MODULE => (String::new(), format!("\n\nconfig =\n    {m}.config\n")),
        Some(m) => (
            format!("import {m}\n"),
            format!("\n\nconfig =\n    {m}.config\n"),
        ),
        None => (String::new(), String::new()),
    };
    format!(
        "module {AI_ENTRY_MODULE} exposing (main)\n\n\
         import Std.Ai.Pipeline as Pipeline\n\
         import {AI_MODULE}\n\
         {import}\n\
         main =\n    Pipeline.commandLine {AI_MODULE}.{AI_VALUE}\n{config}"
    )
}

/// Where the pipeline takes the app's `config` (`Sky.Config`) from: `Ai` when
/// it exposes one, else the app's entry module (sky.toml `entry`) when that
/// defines and exposes one. The second value is a warning when the entry
/// module defines `config` without exposing it: the pipeline cannot apply it,
/// so it would read the database `sky.toml` and the environment name, which
/// may not be the app's.
pub fn config_source(project_dir: &Path) -> (Option<String>, Option<String>) {
    let root = project_dir.join(crate::build::configured_source_root(project_dir));
    if let Ok(src) = std::fs::read_to_string(root.join(format!("{AI_MODULE}.sky"))) {
        if defines_config(&src) && exposes(&src, "config") {
            return (Some(AI_MODULE.to_string()), None);
        }
    }
    let entry = sky_toml_entry(project_dir).unwrap_or_else(|| "src/Main.sky".to_string());
    let file = project_dir.join(&entry);
    let Ok(src) = std::fs::read_to_string(&file) else {
        return (None, None);
    };
    if !defines_config(&src) {
        return (None, None);
    }
    let parse = syntax::parse(&src, base::FileId(0));
    let Some(name) = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
    else {
        return (None, None);
    };
    if exposes(&src, "config") {
        (Some(name), None)
    } else {
        (
            None,
            Some(format!(
                "{entry} defines `config` but does not expose it, so the AI pipeline cannot apply it \
                 and reads the database sky.toml and the environment name; expose it \
                 (`module {name} exposing (main, config)`)"
            )),
        )
    }
}

/// A top-level `config` definition (`config =` or `config :` at column 0).
fn defines_config(src: &str) -> bool {
    src.lines().any(|l| {
        let rest = l.strip_prefix("config").unwrap_or("x");
        let rest = rest.trim_start();
        rest.starts_with('=') || rest.starts_with(':')
    })
}

/// Whether the module header exposes `name` (or everything).
fn exposes(src: &str, name: &str) -> bool {
    header_exposing(src)
        .unwrap_or_default()
        .replace(['(', ')', ','], " ")
        .split_whitespace()
        .any(|w| w == ".." || w == name)
}

/// The top-level `entry` of `sky.toml`.
fn sky_toml_entry(project_dir: &Path) -> Option<String> {
    let text = std::fs::read_to_string(project_dir.join("sky.toml")).ok()?;
    for line in text.lines() {
        let l = line.trim();
        if l.starts_with('[') {
            return None;
        }
        if let Some(rest) = l.strip_prefix("entry") {
            let v = rest.trim_start().strip_prefix('=')?.trim();
            let v = v.split('#').next().unwrap_or("").trim().trim_matches('"');
            return (!v.is_empty()).then(|| v.to_string());
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn project(files: &[(&str, &str)]) -> PathBuf {
        static N: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let dir = std::env::temp_dir().join(format!(
            "sky-ai-entry-{}-{}-{}",
            std::process::id(),
            N.fetch_add(1, std::sync::atomic::Ordering::SeqCst),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos())
                .unwrap_or(0)
        ));
        for (path, body) in files {
            let p = dir.join(path);
            std::fs::create_dir_all(p.parent().unwrap()).unwrap();
            std::fs::write(p, body).unwrap();
        }
        dir
    }

    const TOML: &str =
        "name = \"x\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n";

    #[test]
    fn a_project_without_ai_sky_has_no_entry() {
        let dir = project(&[
            ("sky.toml", TOML),
            ("src/Main.sky", "module Main exposing (main)\n"),
        ]);
        assert_eq!(discover(&dir), Ok(None));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn the_entry_is_found_by_name_or_by_dot_dot() {
        for header in [
            "module Ai exposing (pipeline)\n",
            "module Ai exposing\n    ( other\n    , pipeline -- the lifecycle\n    )\n",
            "module Ai exposing (..)\n",
        ] {
            let dir = project(&[("sky.toml", TOML), ("src/Ai.sky", header)]);
            let found = discover(&dir).unwrap().expect("an entry");
            assert!(found.file.ends_with("src/Ai.sky"), "{header}");
            let _ = std::fs::remove_dir_all(&dir);
        }
    }

    #[test]
    fn a_wrong_module_or_a_missing_export_names_the_fix() {
        for (header, want) in [
            (
                "module Main exposing (pipeline)\n",
                "declares `module Main`",
            ),
            ("module Ai exposing (other)\n", "does not expose `pipeline`"),
            ("x = 1\n", "has no module header"),
        ] {
            let dir = project(&[("sky.toml", TOML), ("src/Ai.sky", header)]);
            let err = discover(&dir).unwrap_err();
            assert!(err.contains(want), "{header}: {err}");
            assert!(err.starts_with("src/Ai.sky: "), "{err}");
            assert!(
                err.contains("module Ai exposing (pipeline)") || err.contains("declares"),
                "{err}"
            );
            let _ = std::fs::remove_dir_all(&dir);
        }
    }

    #[test]
    fn a_custom_source_root_is_used() {
        let toml = "name = \"x\"\nversion = \"0.1.0\"\nentry = \"app/Main.sky\"\n\n[source]\nroot = \"app\"\n";
        let dir = project(&[
            ("sky.toml", toml),
            ("app/Ai.sky", "module Ai exposing (pipeline)\n"),
        ]);
        assert!(discover(&dir).unwrap().is_some());
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn the_pipeline_applies_the_apps_config() {
        let main_exposed = "module Main exposing (main, config)\n\nconfig : Config.Config\nconfig =\n    Config.default\n";
        let main_hidden = "module Main exposing (main)\n\nconfig =\n    Config.default\n";
        let ai = "module Ai exposing (pipeline)\n";
        let ai_config = "module Ai exposing (pipeline, config)\n\nconfig =\n    Config.default\n";
        let dir = project(&[
            ("sky.toml", TOML),
            ("src/Main.sky", main_exposed),
            ("src/Ai.sky", ai),
        ]);
        assert_eq!(config_source(&dir), (Some("Main".to_string()), None));
        let src = entry_source_with(Some("Main"));
        assert!(
            src.contains("import Main\n") && src.contains("config =\n    Main.config"),
            "{src}"
        );
        let _ = std::fs::remove_dir_all(&dir);

        let dir = project(&[
            ("sky.toml", TOML),
            ("src/Main.sky", main_hidden),
            ("src/Ai.sky", ai),
        ]);
        let (from, warning) = config_source(&dir);
        assert_eq!(from, None);
        assert!(warning.unwrap().contains("does not expose it"));
        let _ = std::fs::remove_dir_all(&dir);

        let dir = project(&[
            ("sky.toml", TOML),
            ("src/Main.sky", main_hidden),
            ("src/Ai.sky", ai_config),
        ]);
        assert_eq!(config_source(&dir), (Some("Ai".to_string()), None));
        let src = entry_source_with(Some("Ai"));
        assert!(!src.contains("import Ai\nimport Ai"), "{src}");
        assert!(src.contains("config =\n    Ai.config"), "{src}");
        let _ = std::fs::remove_dir_all(&dir);

        let dir = project(&[
            ("sky.toml", TOML),
            ("src/Main.sky", "module Main exposing (main)\n"),
            ("src/Ai.sky", ai),
        ]);
        assert_eq!(config_source(&dir), (None, None));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn the_entry_calls_the_command_line() {
        let s = entry_source();
        assert!(s.starts_with("module SkyAiEntry__ exposing (main)"), "{s}");
        assert!(s.contains("Pipeline.commandLine Ai.pipeline"), "{s}");
    }
}
