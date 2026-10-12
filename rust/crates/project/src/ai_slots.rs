//! The Std.Ai slots a project declares, read from its source at build time.
//!
//! A slot is named by a string literal (or a top-level `String` constant) given
//! to `Std.Ai.Endpoint.endpoint`: `Endpoint.endpoint "chat"`. Because the name
//! is static, the build can list every AI slot an app can use without running
//! it, and the runtime can say at startup which variables each slot reads and
//! where its settings come from, before the first call resolves it
//! (`docs/design/std-ai-lifecycle.md` §5.2 and §5.5).
//!
//! Two consumers read the list:
//!
//! - the build writes it into the binary as generated Go
//!   ([`SLOTS_GO_FILE`]: an `init()` that calls `rt.AiDeclareSlots`), and the
//!   startup report prints one line per slot;
//! - `sky doctor` checks each slot's variables.
//!
//! A project that imports `Std.Ai.Eval` also has the eval judge's slot,
//! `"judge"`, which the stdlib declares.
//!
//! The scan is textual over each module's import list and call sites, with
//! the argument evaluated by [`crate::app_entry::static_string`], the same
//! evaluator `App.withAppUrl` uses. A slot computed at run time is not listed:
//! it still works, it is only missing from the startup report until its first
//! call.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

/// The generated Go file that declares the slots.
pub const SLOTS_GO_FILE: &str = "ai_slots_gen.go";

/// The slots declared by every `.sky` module under the project's source root,
/// sorted and without duplicates.
pub fn discover(project_dir: &Path) -> Vec<String> {
    discover_with_problems(project_dir).0
}

/// [`discover`], with a warning for each literal slot name the runtime would
/// refuse at the slot's first call: not `[a-z][a-z0-9_]*`, or a reserved word.
pub fn discover_with_problems(project_dir: &Path) -> (Vec<String>, Vec<String>) {
    let root = project_dir.join(crate::build::configured_source_root(project_dir));
    let mut files = Vec::new();
    collect_sky_files(&root, &mut files);
    let mut out = BTreeSet::new();
    let mut problems = BTreeSet::new();
    for f in files {
        if let Ok(src) = std::fs::read_to_string(&f) {
            let (ok, bad) = scan(&src);
            out.extend(ok);
            for b in bad {
                problems.insert(format!(
                    "{}: Endpoint.endpoint {b:?}: {}",
                    f.strip_prefix(project_dir).unwrap_or(&f).display(),
                    slot_problem(&b)
                ));
            }
        }
    }
    (out.into_iter().collect(), problems.into_iter().collect())
}

/// The words a slot may not be: the machine-wide variables' words, so
/// `<PREFIX>_AI_HOME` never reads as a slot called `home`. Mirrors
/// `aiReservedSlots` in runtime-go/rt/ai_config.go (a test compares them).
pub const RESERVED_SLOTS: &[&str] = &[
    "accel", "db", "eval", "fetch", "home", "llama", "offline", "registry", "serve", "train",
    "vllm",
];

fn slot_problem(s: &str) -> &'static str {
    if RESERVED_SLOTS.contains(&s) {
        "a reserved word (it would collide with a machine-wide AI variable); the slot fails at its first call"
    } else {
        "a slot name is lower case, starts with a letter and uses a-z, 0-9 and _; the slot fails at its first call"
    }
}

fn collect_sky_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(Result::ok).map(|e| e.path()).collect();
    entries.sort();
    for p in entries {
        let hidden = p
            .file_name()
            .map(|n| n.to_string_lossy().starts_with('.'))
            .unwrap_or(true);
        if hidden {
            continue;
        }
        if p.is_dir() {
            collect_sky_files(&p, out);
        } else if p.extension().is_some_and(|x| x == "sky") {
            out.push(p);
        }
    }
}

/// The names `endpoint` is reachable by in a module: `Std.Ai.Endpoint.endpoint`
/// itself, `<alias>.endpoint` for `import Std.Ai.Endpoint as <alias>`, and the
/// bare `endpoint` when the import exposes it (or `(..)`).
fn endpoint_names(src: &str) -> Vec<String> {
    let mut names = Vec::new();
    for line in src.lines() {
        let l = line.trim();
        let Some(rest) = l.strip_prefix("import ") else {
            continue;
        };
        let rest = rest.trim();
        let Some(after) = rest.strip_prefix("Std.Ai.Endpoint") else {
            continue;
        };
        if !(after.is_empty() || after.starts_with(char::is_whitespace)) {
            continue;
        }
        names.push("Std.Ai.Endpoint.endpoint".to_string());
        let words: Vec<&str> = after.split_whitespace().collect();
        if let Some(i) = words.iter().position(|w| *w == "as") {
            if let Some(alias) = words.get(i + 1) {
                names.push(format!("{alias}.endpoint"));
            }
        }
        if let Some(exp) = after.split_once("exposing").map(|(_, e)| e) {
            let exp = exp.replace(['(', ')', ','], " ");
            if exp.split_whitespace().any(|w| w == ".." || w == "endpoint") {
                names.push("endpoint".to_string());
            }
        }
    }
    names
}

/// The slots one module's source declares.
pub fn slots_in_source(src: &str) -> Vec<String> {
    scan(src).0
}

/// The valid and the refused literal slot names of one module.
fn scan(src: &str) -> (Vec<String>, Vec<String>) {
    let mut out = Vec::new();
    let mut bad = Vec::new();
    if src.lines().any(|l| {
        let l = l.trim();
        l == "import Std.Ai.Eval" || l.starts_with("import Std.Ai.Eval ")
    }) {
        out.push("judge".to_string());
    }
    let names = endpoint_names(src);
    if names.is_empty() {
        return (out, bad);
    }
    let bytes = src.as_bytes();
    for name in &names {
        let mut from = 0;
        while let Some(pos) = src[from..].find(name.as_str()) {
            let at = from + pos;
            from = at + name.len();
            // A whole name: not the tail of a longer identifier or path.
            if at > 0 {
                let prev = bytes[at - 1] as char;
                if prev.is_ascii_alphanumeric() || prev == '_' || prev == '.' {
                    continue;
                }
            }
            if from < bytes.len() {
                let next = bytes[from] as char;
                if next.is_ascii_alphanumeric() || next == '_' || next == '.' {
                    continue;
                }
            }
            // Skip a type annotation or a definition (`endpoint :`, `endpoint =`).
            let rest = src[from..].trim_start();
            if rest.starts_with(':') || rest.starts_with('=') {
                continue;
            }
            if let Some(arg) = first_argument(rest) {
                let text = crate::app_entry::ArgText { text: arg, col: 0 };
                if let Ok(slot) = crate::app_entry::static_string(src, &text) {
                    if valid_slot(&slot) && !RESERVED_SLOTS.contains(&slot.as_str()) {
                        out.push(slot);
                    } else {
                        bad.push(slot);
                    }
                }
            }
        }
    }
    out.sort();
    out.dedup();
    bad.sort();
    bad.dedup();
    (out, bad)
}

/// The first argument after a function name: a string literal or an
/// identifier.
fn first_argument(rest: &str) -> Option<String> {
    if let Some(body) = rest.strip_prefix('"') {
        let mut escaped = false;
        for (i, c) in body.char_indices() {
            match c {
                '\\' if !escaped => escaped = true,
                '"' if !escaped => return Some(format!("\"{}\"", &body[..i])),
                '\n' => return None,
                _ => escaped = false,
            }
        }
        return None;
    }
    let ident: String = rest
        .chars()
        .take_while(|c| c.is_ascii_alphanumeric() || *c == '_')
        .collect();
    if ident.starts_with(|c: char| c.is_ascii_lowercase()) {
        Some(ident)
    } else {
        None
    }
}

/// `[a-z][a-z0-9_]*`, as the runtime requires (`aiValidSlot`).
fn valid_slot(s: &str) -> bool {
    let mut chars = s.chars();
    matches!(chars.next(), Some(c) if c.is_ascii_lowercase())
        && chars.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_')
}

/// Write [`SLOTS_GO_FILE`] into the build directory, or remove a stale one
/// when the project declares no slot.
pub fn write_go(out_dir: &Path, slots: &[String]) -> Result<(), String> {
    let target = out_dir.join(SLOTS_GO_FILE);
    if slots.is_empty() {
        let _ = std::fs::remove_file(&target);
        return Ok(());
    }
    let list = slots
        .iter()
        .map(|s| format!("{s:?}"))
        .collect::<Vec<_>>()
        .join(", ");
    let go = format!(
        "package main\n\nimport rt \"sky-app/rt\"\n\n\
         // Generated by `sky build` from the Std.Ai.Endpoint slots the source\n\
         // names — do not edit. The startup report prints one line per slot.\n\
         func init() {{\n\
         \trt.AiDeclareSlots([]string{{{list}}})\n\
         }}\n"
    );
    std::fs::write(&target, go).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn literal_alias_and_constant_slots_are_found() {
        let src = r#"module Main exposing (main)

import Std.Ai.Endpoint as Endpoint exposing (Endpoint)

chatName : String
chatName =
    "chat"

chat : Endpoint
chat =
    Endpoint.endpoint chatName

embed =
    Endpoint.endpoint "embed" |> Endpoint.withUrl "x"

computed n =
    Endpoint.endpoint (n ++ "x")
"#;
        assert_eq!(slots_in_source(src), vec!["chat", "embed"]);
    }

    #[test]
    fn exposed_endpoint_and_the_judge_are_found() {
        let src = r#"module Ai exposing (..)

import Std.Ai.Endpoint exposing (endpoint)
import Std.Ai.Eval as Eval

support =
    endpoint "support"
"#;
        assert_eq!(slots_in_source(src), vec!["judge", "support"]);
    }

    #[test]
    fn a_module_without_the_import_declares_nothing() {
        let src = "module M exposing (..)\n\nendpoint = \"x\"\n\nf = endpoint \"chat\"\n";
        assert!(slots_in_source(src).is_empty());
    }

    #[test]
    fn an_invalid_or_reserved_slot_name_is_not_listed_but_reported() {
        let src = "import Std.Ai.Endpoint as E\n\na = E.endpoint \"Chat\"\nb = E.endpoint \"ok_1\"\nc = E.endpoint \"home\"\n";
        assert_eq!(slots_in_source(src), vec!["ok_1"]);
        assert_eq!(scan(src).1, vec!["Chat", "home"]);
        assert!(slot_problem("home").contains("reserved"));
        assert!(slot_problem("Chat").contains("lower case"));
    }

    #[test]
    fn the_reserved_words_match_the_runtime() {
        let go = std::fs::read_to_string(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../runtime-go/rt/ai_config.go"),
        )
        .unwrap();
        let start = go.find("var aiReservedSlots = map[string]bool{").unwrap();
        let block = &go[start..start + go[start..].find('}').unwrap()];
        let mut words: Vec<&str> = block
            .split('"')
            .enumerate()
            .filter(|(i, _)| i % 2 == 1)
            .map(|(_, w)| w)
            .collect();
        words.sort();
        assert_eq!(words, RESERVED_SLOTS);
    }

    #[test]
    fn the_go_file_is_written_and_removed() {
        let dir = std::env::temp_dir().join(format!("sky-ai-slots-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        write_go(&dir, &["chat".to_string(), "judge".to_string()]).unwrap();
        let go = std::fs::read_to_string(dir.join(SLOTS_GO_FILE)).unwrap();
        assert!(
            go.contains(r#"rt.AiDeclareSlots([]string{"chat", "judge"})"#),
            "{go}"
        );
        write_go(&dir, &[]).unwrap();
        assert!(!dir.join(SLOTS_GO_FILE).exists());
        let _ = std::fs::remove_dir_all(&dir);
    }
}
