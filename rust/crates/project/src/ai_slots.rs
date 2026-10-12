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
//! `"judge"`, and one that imports `Std.Ai.Dataset.Synthetic` the teacher's,
//! `"teacher"`, which the stdlib declares.
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

/// The slots of the modules an entry reaches through `import`, itself
/// included: the program a build makes can only use those. An app's build
/// (entry `Main`) does not list the slots only its AI pipeline
/// (`src/Ai.sky`) names, such as a synthetic teacher or a judge, unless the
/// app imports that module; the pipeline's own program (entry
/// `SkyAiEntry__`, which imports `Ai`) lists them. `extra_roots` are further
/// source roots (`tests/`, a synthesised entry's scratch directory). The
/// entry `Main` also matches a module named `<X>.Main` or `main`, as the
/// build's entry detection does.
pub fn discover_for_entry(
    project_dir: &Path,
    extra_roots: &[PathBuf],
    entry: &str,
) -> (Vec<String>, Vec<String>) {
    let root = project_dir.join(crate::build::configured_source_root(project_dir));
    let mut files = Vec::new();
    collect_sky_files(&root, &mut files);
    for r in extra_roots {
        collect_sky_files(r, &mut files);
    }
    let mut by_name: std::collections::HashMap<String, Vec<(PathBuf, String)>> =
        std::collections::HashMap::new();
    for f in files {
        let Ok(src) = std::fs::read_to_string(&f) else {
            continue;
        };
        let Some(name) = module_name_of(&src) else {
            continue;
        };
        by_name.entry(name).or_default().push((f, src));
    }
    let mut stack: Vec<String> = by_name
        .keys()
        .filter(|n| {
            n.as_str() == entry
                || (entry == "Main" && (n.ends_with(".Main") || n.as_str() == "main"))
        })
        .cloned()
        .collect();
    if stack.is_empty() {
        // No module is the entry (a generated project that names its entry
        // otherwise): every module's slots, as before scoping.
        return discover_with_problems(project_dir);
    }
    let mut reached = BTreeSet::new();
    let mut out = BTreeSet::new();
    let mut problems = BTreeSet::new();
    while let Some(name) = stack.pop() {
        if !reached.insert(name.clone()) {
            continue;
        }
        let Some(mods) = by_name.get(&name) else {
            continue;
        };
        for (f, src) in mods {
            let (ok, bad) = scan(src);
            out.extend(ok);
            for b in bad {
                problems.insert(format!(
                    "{}: Endpoint.endpoint {b:?}: {}",
                    f.strip_prefix(project_dir).unwrap_or(f).display(),
                    slot_problem(&b)
                ));
            }
            for imported in imports_of(src) {
                if !reached.contains(&imported) {
                    stack.push(imported);
                }
            }
        }
    }
    (out.into_iter().collect(), problems.into_iter().collect())
}

/// The name a module declares (`module A.B exposing ...`), read from the
/// first non-comment line that starts the header.
fn module_name_of(src: &str) -> Option<String> {
    for line in src.lines() {
        let l = line.trim_start();
        if let Some(rest) = l.strip_prefix("module ") {
            let name: String = rest
                .chars()
                .take_while(|c| c.is_ascii_alphanumeric() || *c == '_' || *c == '.')
                .collect();
            return (!name.is_empty()).then_some(name);
        }
    }
    None
}

/// The modules a source imports.
fn imports_of(src: &str) -> Vec<String> {
    src.lines()
        .filter_map(|line| {
            let rest = line.strip_prefix("import ")?;
            let name: String = rest
                .trim_start()
                .chars()
                .take_while(|c| c.is_ascii_alphanumeric() || *c == '_' || *c == '.')
                .collect();
            (!name.is_empty()).then_some(name)
        })
        .collect()
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
    // The slots the stdlib declares: Std.Ai.Eval's judge and
    // Std.Ai.Dataset.Synthetic's teacher (`Eval.judgeSlot`,
    // `Synthetic.teacherSlot`), which a module uses by importing it.
    let imports = |m: &str| {
        src.lines().any(|l| {
            let l = l.trim();
            l == format!("import {m}") || l.starts_with(&format!("import {m} "))
        })
    };
    if imports("Std.Ai.Eval") {
        out.push("judge".to_string());
    }
    if imports("Std.Ai.Dataset.Synthetic") {
        out.push("teacher".to_string());
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

/// One Std.Ai device slot: a top-level zero-parameter binding whose value is
/// an `Std.Ai.Endpoint` given `Endpoint.withDevice`. The lowering roots the
/// binding and `main` registers it (`rt.AiDeviceRegister`), so the server
/// answers `/_sky/ai/device/<slot>` and a server-side `Device.provider` finds
/// it (`docs/ai/device.md`).
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct DeviceSlot {
    pub module: String,
    pub binding: String,
    pub slot: String,
}

/// The device slots and the `Device.provider` slot names of the modules an
/// entry reaches, with the build errors they raise:
///
/// 1. `Endpoint.withDevice` outside a top-level zero-parameter binding, or in
///    a binding that does not name its slot with `Endpoint.endpoint "<lit>"`
///    (the build must know the slot without running the program);
/// 2. two device slots with one name;
/// 3. `Device.provider` with a slot that is not a literal (or a top-level
///    `String` constant), and, when `require_server` is set, a
///    `Device.provider "x"` with no device slot `"x"` in the program.
///
/// `require_server` is off for a generated Sky.Spa half: the client half
/// holds the `Device.provider` calls and the server half the slots, and the
/// user's own source is checked whole before the split.
pub fn device_slots_for_entry(
    project_dir: &Path,
    extra_roots: &[PathBuf],
    entry: &str,
    require_server: bool,
) -> (Vec<DeviceSlot>, Vec<String>) {
    let mut slots: Vec<DeviceSlot> = Vec::new();
    let mut wanted: Vec<(String, String)> = Vec::new(); // (where, slot)
    let mut errors: BTreeSet<String> = BTreeSet::new();
    for (module, f, src) in reached_sources(project_dir, extra_roots, entry) {
        let shown = f
            .strip_prefix(project_dir)
            .unwrap_or(&f)
            .display()
            .to_string();
        let (found, used, errs) = scan_device(&module, &src);
        for e in errs {
            errors.insert(format!("{shown}: {e}"));
        }
        slots.extend(found);
        wanted.extend(used.into_iter().map(|u| (shown.clone(), u)));
    }
    slots.sort();
    slots.dedup();
    for w in slots.windows(2) {
        if w[0].slot == w[1].slot {
            errors.insert(format!(
                "two device slots are named {:?}: {}.{} and {}.{}; a slot name names one slot",
                w[0].slot, w[0].module, w[0].binding, w[1].module, w[1].binding
            ));
        }
    }
    if require_server {
        for (shown, slot) in &wanted {
            if !slots.iter().any(|d| &d.slot == slot) {
                errors.insert(format!(
                    "{shown}: Device.provider {slot:?} has no server slot: give an \
                     `Endpoint.endpoint {slot:?}` binding `Endpoint.withDevice` in a module the \
                     program imports"
                ));
            }
        }
    }
    (slots, errors.into_iter().collect())
}

/// Every module the entry reaches through `import`, with its file and source.
/// Every module when no module is the entry.
fn reached_sources(
    project_dir: &Path,
    extra_roots: &[PathBuf],
    entry: &str,
) -> Vec<(String, PathBuf, String)> {
    let root = project_dir.join(crate::build::configured_source_root(project_dir));
    let mut files = Vec::new();
    collect_sky_files(&root, &mut files);
    for r in extra_roots {
        collect_sky_files(r, &mut files);
    }
    let mut by_name: std::collections::BTreeMap<String, Vec<(PathBuf, String)>> =
        std::collections::BTreeMap::new();
    for f in files {
        let Ok(src) = std::fs::read_to_string(&f) else {
            continue;
        };
        let Some(name) = module_name_of(&src) else {
            continue;
        };
        by_name.entry(name).or_default().push((f, src));
    }
    let mut stack: Vec<String> = by_name
        .keys()
        .filter(|n| {
            n.as_str() == entry
                || (entry == "Main" && (n.ends_with(".Main") || n.as_str() == "main"))
        })
        .cloned()
        .collect();
    if stack.is_empty() {
        stack = by_name.keys().cloned().collect();
    }
    let mut reached = BTreeSet::new();
    let mut out = Vec::new();
    while let Some(name) = stack.pop() {
        if !reached.insert(name.clone()) {
            continue;
        }
        let Some(mods) = by_name.get(&name) else {
            continue;
        };
        for (f, src) in mods {
            for imported in imports_of(src) {
                if !reached.contains(&imported) {
                    stack.push(imported);
                }
            }
            out.push((name.clone(), f.clone(), src.clone()));
        }
    }
    out.sort();
    out
}

/// The names a module reaches a member of `module` by: `<module>.<member>`,
/// `<alias>.<member>`, and the bare member when the import exposes it.
fn member_names(src: &str, module: &str, member: &str) -> Vec<String> {
    let mut names = Vec::new();
    for line in src.lines() {
        let Some(rest) = line.trim().strip_prefix("import ") else {
            continue;
        };
        let Some(after) = rest.trim().strip_prefix(module) else {
            continue;
        };
        if !(after.is_empty() || after.starts_with(char::is_whitespace)) {
            continue;
        }
        names.push(format!("{module}.{member}"));
        let words: Vec<&str> = after.split_whitespace().collect();
        if let Some(i) = words.iter().position(|w| *w == "as") {
            if let Some(alias) = words.get(i + 1) {
                names.push(format!("{alias}.{member}"));
            }
        }
        if let Some(exp) = after.split_once("exposing").map(|(_, e)| e) {
            let exp = exp.replace(['(', ')', ','], " ");
            if exp.split_whitespace().any(|w| w == ".." || w == member) {
                names.push(member.to_string());
            }
        }
    }
    names
}

/// The byte offsets just after each whole-word use of one of `names` in
/// `text` that is a call or a reference (not its own definition or
/// annotation).
fn uses_of(text: &str, names: &[String]) -> Vec<usize> {
    let bytes = text.as_bytes();
    let mut out = Vec::new();
    for name in names {
        let mut from = 0;
        while let Some(pos) = text[from..].find(name.as_str()) {
            let at = from + pos;
            from = at + name.len();
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
            let rest = text[from..].trim_start();
            if rest.starts_with(':') || (rest.starts_with('=') && !rest.starts_with("==")) {
                continue;
            }
            out.push(from);
        }
    }
    out.sort_unstable();
    out.dedup();
    out
}

/// One module's device slots, its `Device.provider` slot names, and its
/// errors.
fn scan_device(module: &str, src: &str) -> (Vec<DeviceSlot>, Vec<String>, Vec<String>) {
    let mut slots = Vec::new();
    let mut used = Vec::new();
    let mut errors = Vec::new();
    let with_device = member_names(src, "Std.Ai.Endpoint", "withDevice");
    let device_provider = member_names(src, "Std.Ai.Device", "provider");
    let endpoints = endpoint_names(src);
    if with_device.is_empty() && device_provider.is_empty() {
        return (slots, used, errors);
    }
    // `Device.provider "x"` anywhere in the module.
    for at in uses_of(src, &device_provider) {
        let rest = src[at..].trim_start();
        let slot = first_argument(rest).and_then(|arg| {
            let text = crate::app_entry::ArgText { text: arg, col: 0 };
            crate::app_entry::static_string(src, &text).ok()
        });
        match slot {
            Some(s) if valid_slot(&s) && !RESERVED_SLOTS.contains(&s.as_str()) => used.push(s),
            Some(s) => errors.push(format!("Device.provider {s:?}: {}", slot_problem(&s))),
            None => errors.push(
                "Device.provider needs its slot as a string literal or a top-level String \
                 constant, so the build can find the server slot"
                    .to_string(),
            ),
        }
    }
    if with_device.is_empty() {
        return (slots, used, errors);
    }
    // `Endpoint.withDevice` only in a top-level zero-parameter binding that
    // names its slot.
    let file = crate::app_entry::parse_source(src);
    let mut covered: Vec<(usize, usize)> = Vec::new();
    for d in file.decls() {
        let syntax::ast::Decl::Value(v) = d else {
            continue;
        };
        let Some(name) = v.name() else {
            continue;
        };
        let range = syntax::ast::AstNode::syntax(&v).text_range();
        let (start, end) = (usize::from(range.start()), usize::from(range.end()));
        let body = &src[start..end];
        if uses_of(body, &with_device).is_empty() {
            continue;
        }
        covered.push((start, end));
        let binding = name.text().to_string();
        let has_params = v
            .params()
            .map(|p| {
                syntax::ast::AstNode::syntax(&p)
                    .text()
                    .to_string()
                    .trim()
                    .len()
                    > 0
            })
            .unwrap_or(false);
        if has_params {
            errors.push(format!(
                "{binding}: Endpoint.withDevice is only allowed in a top-level binding without \
                 parameters (`chatSlot = Endpoint.endpoint \"chat\" |> Endpoint.withDevice …`), \
                 so the server can register the slot when it starts"
            ));
            continue;
        }
        let mut slot = None;
        for at in uses_of(body, &endpoints) {
            let rest = body[at..].trim_start();
            if let Some(arg) = first_argument(rest) {
                let text = crate::app_entry::ArgText { text: arg, col: 0 };
                if let Ok(s) = crate::app_entry::static_string(src, &text) {
                    slot = Some(s);
                    break;
                }
            }
        }
        match slot {
            Some(s) if valid_slot(&s) && !RESERVED_SLOTS.contains(&s.as_str()) => {
                slots.push(DeviceSlot {
                    module: module.to_string(),
                    binding,
                    slot: s,
                })
            }
            Some(s) => errors.push(format!("{binding}: slot {s:?}: {}", slot_problem(&s))),
            None => errors.push(format!(
                "{binding}: a binding with Endpoint.withDevice names its slot in the same \
                 binding with Endpoint.endpoint and a string literal"
            )),
        }
    }
    for at in uses_of(src, &with_device) {
        if !covered.iter().any(|(s, e)| *s <= at && at <= *e) {
            errors.push(
                "Endpoint.withDevice is only allowed in a top-level binding without parameters"
                    .to_string(),
            );
            break;
        }
    }
    (slots, used, errors)
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

    /// A synthetic generator's default teacher is the stdlib's
    /// `"teacher"` slot: a module that imports the generator uses it.
    #[test]
    fn the_synthetic_teacher_is_found() {
        let src = "module Data exposing (..)\n\nimport Std.Ai.Dataset.Synthetic as Synthetic\n\ng = Synthetic.generator \"x\"\n";
        assert_eq!(slots_in_source(src), vec!["teacher"]);
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

    /// An app's build lists the slots its entry reaches, not the ones only
    /// the AI pipeline names; the pipeline's entry lists both.
    #[test]
    fn slots_are_scoped_to_the_entry_closure() {
        let dir = std::env::temp_dir().join(format!(
            "sky-ai-slots-scope-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos())
                .unwrap_or(0)
        ));
        let src = dir.join("src");
        std::fs::create_dir_all(&src).unwrap();
        std::fs::write(
            dir.join("sky.toml"),
            "name = \"x\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
        )
        .unwrap();
        std::fs::write(
            src.join("Main.sky"),
            "module Main exposing (main)\n\nimport Assist\n\nmain = Assist.reply\n",
        )
        .unwrap();
        std::fs::write(
            src.join("Assist.sky"),
            "module Assist exposing (chatSlot)\n\nimport Std.Ai.Endpoint as Endpoint\n\nchatSlot = Endpoint.endpoint \"chat\"\n",
        )
        .unwrap();
        std::fs::write(
            src.join("Ai.sky"),
            "module Ai exposing (pipeline)\n\nimport Assist\nimport Data\nimport Std.Ai.Eval as Eval\n\npipeline = 1\n",
        )
        .unwrap();
        std::fs::write(
            src.join("Data.sky"),
            "module Data exposing (teacher)\n\nimport Std.Ai.Endpoint as E\n\nteacher = E.endpoint \"teacher\"\n",
        )
        .unwrap();
        let scratch = dir.join("scratch");
        std::fs::create_dir_all(&scratch).unwrap();
        std::fs::write(
            scratch.join("SkyAiEntry__.sky"),
            "module SkyAiEntry__ exposing (main)\n\nimport Std.Ai.Pipeline as Pipeline\nimport Ai\n\nmain = Pipeline.commandLine Ai.pipeline\n",
        )
        .unwrap();
        assert_eq!(discover_for_entry(&dir, &[], "Main").0, vec!["chat"]);
        assert_eq!(
            discover_for_entry(&dir, &[scratch], "SkyAiEntry__").0,
            vec!["chat", "judge", "teacher"]
        );
        // Every module (the unscoped scan `sky doctor` used) still has all.
        assert_eq!(discover(&dir), vec!["chat", "judge", "teacher"]);
        let _ = std::fs::remove_dir_all(&dir);
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
