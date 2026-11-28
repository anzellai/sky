//! A project type named like a stdlib type never captures a stdlib value.
//!
//! The class: a module declares `type alias Set = { zz : Int }` and calls
//! `Set.fromList [ 1, 2 ]`. The checker is right (the value is the kernel
//! `Set`), but the lowering read the BARE kernel name `Set` as the module's own
//! alias, coerced the runtime set to `Main_Set_R`, and the program panicked
//! (`CoerceFailure: expected main.Main_Set_R, got rt.SkySet`).
//!
//! The test closes the class for every type name, not one: it reads every
//! exported signature of `sky-stdlib/` (the one source of the stdlib types,
//! kernel modules included), collects each type name the signatures mention,
//! and for each name picks an exported function that gives a value whose type
//! mentions that name and whose arguments are plain literals. It generates one
//! program that calls each such function, three times:
//!
//! 1. the control, with no project types,
//! 2. with `type alias <Name> = { zz : Int }` for every name,
//! 3. with `type <Name> = <Name>C Int` for every name.
//!
//! Each program is built AND run, and all three must print the same line. The
//! prelude-protected names (`Int`, `Maybe`, `Error`, ...) cannot be declared:
//! a fourth program declares them all and must fail with `[E1004]` for each.
//! Needs a `go` toolchain.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// `hir::PRELUDE_PROTECTED`'s type names: a project type of one of these names
/// is `[E1004]`.
const PROTECTED: &[&str] = &[
    "Int", "Float", "Bool", "String", "Char", "List", "Maybe", "Result", "Task", "Error",
];

/// Modules the generated program does not import: `Sky.Ffi` is stdlib-only
/// (`[E1011]`), and a reference to an app module makes the program an app
/// entry with its own target rules (Sky.Live, Sky.Spa, a terminal), which
/// the app flows test.
const NOT_IMPORTABLE: &[&str] = &["Sky.Ffi", "Std.Spa"];

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(3)
        .unwrap()
        .to_path_buf()
}

fn sky_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<_> = std::fs::read_dir(dir).unwrap().flatten().collect();
    entries.sort_by_key(|e| e.path());
    for e in entries {
        let p = e.path();
        if p.is_dir() {
            sky_files(&p, out);
        } else if p.extension().is_some_and(|x| x == "sky") {
            out.push(p);
        }
    }
}

/// The text between the module header's `exposing (` and its matching `)`.
fn exposing_list(src: &str) -> Option<Vec<String>> {
    let start = src.find("exposing")?;
    let open = start + src[start..].find('(')?;
    let mut depth = 0usize;
    let mut end = open;
    for (i, c) in src[open..].char_indices() {
        match c {
            '(' => depth += 1,
            ')' => {
                depth -= 1;
                if depth == 0 {
                    end = open + i;
                    break;
                }
            }
            _ => {}
        }
    }
    let inner = &src[open + 1..end];
    if inner.trim() == ".." {
        return None;
    }
    Some(
        inner
            .split(',')
            .map(|s| s.trim().split('(').next().unwrap_or("").trim().to_string())
            .filter(|s| !s.is_empty())
            .collect(),
    )
}

/// Every top-level `name : Type` signature, continuation lines joined.
fn signatures(src: &str) -> Vec<(String, String)> {
    let mut out = Vec::new();
    let lines: Vec<&str> = src.lines().collect();
    let mut i = 0;
    while i < lines.len() {
        let l = lines[i];
        let head = l.split(" : ").next().unwrap_or("");
        let is_sig = l.contains(" : ")
            && head.chars().next().is_some_and(|c| c.is_ascii_lowercase())
            && head
                .chars()
                .all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '\'');
        if is_sig {
            let mut ty = l[head.len() + 3..].to_string();
            while i + 1 < lines.len() && lines[i + 1].starts_with(' ') {
                i += 1;
                ty.push(' ');
                ty.push_str(lines[i].trim());
            }
            out.push((head.to_string(), ty));
        }
        i += 1;
    }
    out
}

/// Split a type at its top-level arrows.
fn arrows(ty: &str) -> Vec<String> {
    let mut parts = Vec::new();
    let mut depth = 0i32;
    let mut cur = String::new();
    let cs: Vec<char> = ty.chars().collect();
    let mut i = 0;
    while i < cs.len() {
        let c = cs[i];
        if (c == '(' || c == '{' || c == '[') {
            depth += 1;
        } else if c == ')' || c == '}' || c == ']' {
            depth -= 1;
        }
        if depth == 0 && c == '-' && i + 1 < cs.len() && cs[i + 1] == '>' {
            parts.push(cur.trim().to_string());
            cur.clear();
            i += 2;
            continue;
        }
        cur.push(c);
        i += 1;
    }
    parts.push(cur.trim().to_string());
    parts
}

/// The base names of the type constructors a type mentions.
fn type_names(ty: &str) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    let mut word = String::new();
    for c in ty.chars().chain(std::iter::once(' ')) {
        if c.is_ascii_alphanumeric() || c == '_' || c == '.' {
            word.push(c);
        } else {
            if word.starts_with(|c: char| c.is_ascii_uppercase()) {
                let base = word.rsplit('.').next().unwrap_or(&word).to_string();
                if base.starts_with(|c: char| c.is_ascii_uppercase()) {
                    out.insert(base);
                }
            }
            word.clear();
        }
    }
    out
}

/// A literal of the argument type, when it is one of the plain shapes.
fn literal(arg: &str) -> Option<&'static str> {
    let a = arg.trim();
    let a = a
        .strip_prefix('(')
        .and_then(|x| x.strip_suffix(')'))
        .filter(|x| !x.contains(',') && !x.contains("->") && !x.is_empty())
        .unwrap_or(a)
        .trim();
    match a {
        "Int" => Some("0"),
        "Float" => Some("0.0"),
        "String" => Some("\"a\""),
        "Bool" => Some("True"),
        "()" => Some("()"),
        _ if a.starts_with("List ") && !a.contains("->") => Some("[]"),
        _ if a.starts_with("Maybe ") && !a.contains("->") => Some("Nothing"),
        _ => None,
    }
}

/// How the program reaches a value whose type mentions the name: a call
/// with literal arguments (`args`), else a reference to the function itself
/// (`reference`), whose Go type the lowering writes all the same.
struct Call {
    module: String,
    func: String,
    args: Vec<&'static str>,
    reference: bool,
}

/// Calls the generator must not make: the build checks their literal
/// arguments against the program (`Device.provider "a"` needs a server slot
/// `a`). They are reached as references instead.
const NO_CALL: &[(&str, &str)] = &[("Std.Ai.Device", "provider")];

/// For every type name the exported stdlib signatures mention, the exported
/// function with the fewest literal arguments whose result mentions it.
fn plan() -> (BTreeSet<String>, BTreeMap<String, Call>) {
    let root = repo_root().join("sky-stdlib");
    let mut files = Vec::new();
    sky_files(&root, &mut files);
    let mut all_names = BTreeSet::new();
    let mut best: BTreeMap<String, Call> = BTreeMap::new();
    for f in files {
        let src = std::fs::read_to_string(&f).unwrap();
        let module = f
            .strip_prefix(&root)
            .unwrap()
            .with_extension("")
            .to_string_lossy()
            .replace('/', ".");
        let exposed = exposing_list(&src);
        for (func, ty) in signatures(&src) {
            let visible = exposed
                .as_ref()
                .is_none_or(|e| e.iter().any(|n| *n == func));
            if !visible {
                continue;
            }
            all_names.extend(type_names(&ty));
            if NOT_IMPORTABLE.contains(&module.as_str()) {
                continue;
            }
            let parts = arrows(&ty);
            let (result, args) = parts.split_last().unwrap();
            let lits = args.iter().map(|a| literal(a)).collect::<Option<Vec<_>>>();
            let callable = !NO_CALL.contains(&(module.as_str(), func.as_str()));
            // A call when its result mentions the name; else a reference to
            // any function whose signature mentions it.
            let (reach, args, reference) = match lits {
                Some(l) if callable => (type_names(result), l, false),
                _ => (type_names(&ty), Vec::new(), true),
            };
            for name in reach {
                // rank: a call before a reference, then fewer arguments, then
                // the module and function name (deterministic)
                let rank =
                    |r: bool, n: usize, m: &str, f: &str| (r, n, m.to_string(), f.to_string());
                let better = match best.get(&name) {
                    None => true,
                    Some(c) => {
                        rank(reference, args.len(), &module, &func)
                            < rank(c.reference, c.args.len(), &c.module, &c.func)
                    }
                };
                if better {
                    best.insert(
                        name,
                        Call {
                            module: module.clone(),
                            func: func.clone(),
                            args: args.clone(),
                            reference,
                        },
                    );
                }
            }
            if reference {
                continue;
            }
            // A callable function also reaches every other name its
            // signature mentions, as a reference.
            for name in type_names(&ty) {
                best.entry(name).or_insert_with(|| Call {
                    module: module.clone(),
                    func: func.clone(),
                    args: Vec::new(),
                    reference: true,
                });
            }
        }
    }
    (all_names, best)
}

#[derive(Clone, Copy, PartialEq)]
enum Variant {
    Control,
    Alias,
    Union,
}

fn program(calls: &BTreeMap<String, Call>, variant: Variant) -> String {
    let mut modules: Vec<&String> = calls.values().map(|c| &c.module).collect();
    modules.sort();
    modules.dedup();
    let qual: BTreeMap<&String, String> = modules
        .iter()
        .enumerate()
        .map(|(i, m)| (*m, format!("Q{i}")))
        .collect();
    let mut s = String::from("module Main exposing (main)\n\n");
    for m in &modules {
        s.push_str(&format!("import {} as {}\n", m, qual[*m]));
    }
    s.push_str("import Std.Log exposing (println)\n\n\n");
    let names: Vec<&String> = calls.keys().collect();
    for (i, n) in names.iter().enumerate() {
        match variant {
            Variant::Control => {}
            Variant::Alias => s.push_str(&format!(
                "type alias {n} =\n    {{ zz : Int }}\n\n\nu{i} : {n}\nu{i} =\n    {{ zz = 1 }}\n\n\n"
            )),
            Variant::Union => s.push_str(&format!(
                "type {n}\n    = {n}C Int\n\n\nu{i} : {n}\nu{i} =\n    {n}C 1\n\n\n\
                 z{i} : {n} -> Int\nz{i} v =\n    case v of\n        {n}C k ->\n            k\n\n\n"
            )),
        }
    }
    s.push_str("main =\n    let\n");
    for (i, n) in names.iter().enumerate() {
        let c = &calls[*n];
        let mut call = format!("{}.{}", qual[&c.module], c.func);
        if !c.reference {
            for a in &c.args {
                call.push(' ');
                call.push_str(a);
            }
        }
        s.push_str(&format!("        v{i} =\n            {call}\n\n"));
    }
    s.push_str("        shown =\n            [ ");
    let shown: Vec<String> = (0..names.len()).map(|i| format!("toString v{i}")).collect();
    s.push_str(&shown.join("\n            , "));
    s.push_str("\n            ]\n\n        own =\n            ");
    let own: Vec<String> = (0..names.len())
        .map(|i| match variant {
            Variant::Control => "1".to_string(),
            Variant::Alias => format!("u{i}.zz"),
            Variant::Union => format!("z{i} u{i}"),
        })
        .collect();
    s.push_str(&own.join(" + "));
    s.push_str(
        "\n    in\n    println (String.fromInt (List.length shown) ++ \" \" ++ String.fromInt own)\n",
    );
    s
}

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-type-capture-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"capture\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    dir
}

/// Run `cmd` to completion, killed after `limit`.
fn bounded(cmd: &mut Command, limit: Duration) -> (bool, String) {
    let mut child = cmd
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .expect("spawn");
    let start = Instant::now();
    loop {
        if let Some(_status) = child.try_wait().unwrap() {
            break;
        }
        if start.elapsed() > limit {
            let _ = child.kill();
            let _ = child.wait();
            return (false, format!("timed out after {limit:?}"));
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    let out = child.wait_with_output().unwrap();
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    (out.status.success(), text)
}

/// Build and run the program; its last output line, or the failure.
fn build_and_run(tag: &str, src: &str) -> Result<String, String> {
    let dir = scratch(tag);
    std::fs::write(dir.join("src/Main.sky"), src).unwrap();
    let (ok, log) = bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&dir),
        Duration::from_secs(900),
    );
    if !ok {
        return Err(format!("{tag}: build failed ({}):\n{log}", dir.display()));
    }
    let (ok, out) = bounded(
        Command::new(dir.join("sky-out/app")).current_dir(&dir),
        Duration::from_secs(120),
    );
    if !ok {
        return Err(format!("{tag}: run failed ({}):\n{out}", dir.display()));
    }
    let _ = std::fs::remove_dir_all(&dir);
    Ok(out.lines().last().unwrap_or("").to_string())
}

#[test]
fn a_project_type_named_like_any_stdlib_type_never_captures_a_stdlib_value() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let (all_names, mut calls) = plan();
    for p in PROTECTED {
        calls.remove(*p);
    }
    // Every name must be covered by a call, or be named here with its reason.
    let uncovered: Vec<&String> = all_names
        .iter()
        .filter(|n| !calls.contains_key(*n) && !PROTECTED.contains(&n.as_str()))
        .collect();
    let referenced = calls.values().filter(|c| c.reference).count();
    eprintln!(
        "{} stdlib type names: {} reached by a call, {} by a function reference, {} protected",
        all_names.len(),
        calls.len() - referenced,
        referenced,
        PROTECTED.len(),
    );
    assert!(
        uncovered.is_empty(),
        "every stdlib type name is reached: {uncovered:?}"
    );
    assert!(
        calls.contains_key("Set") && calls.len() >= 40,
        "the plan must cover Set and the stdlib's type names: {:?}",
        calls.keys().collect::<Vec<_>>()
    );
    let expected = format!("{} {}", calls.len(), calls.len());
    let control = build_and_run("control", &program(&calls, Variant::Control));
    assert_eq!(control, Ok(expected.clone()), "the control program");
    let alias = build_and_run("alias", &program(&calls, Variant::Alias));
    assert_eq!(
        alias,
        Ok(expected.clone()),
        "a record alias per stdlib type name"
    );
    let union = build_and_run("union", &program(&calls, Variant::Union));
    assert_eq!(union, Ok(expected), "a union per stdlib type name");
}

#[test]
fn a_prelude_type_name_cannot_be_declared() {
    let dir = scratch("protected");
    let mut src =
        String::from("module Main exposing (main)\n\nimport Std.Log exposing (println)\n\n\n");
    for p in PROTECTED {
        src.push_str(&format!("type alias {p} =\n    {{ zz : () }}\n\n\n"));
    }
    src.push_str("main =\n    println \"ok\"\n");
    std::fs::write(dir.join("src/Main.sky"), &src).unwrap();
    let (ok, log) = bounded(
        Command::new(SKY)
            .args(["check", "src/Main.sky"])
            .current_dir(&dir),
        Duration::from_secs(300),
    );
    assert!(!ok, "a prelude type name must be refused:\n{log}");
    assert_eq!(
        log.matches("[E1004]").count(),
        PROTECTED.len(),
        "one [E1004] per prelude name:\n{log}"
    );
    for p in PROTECTED {
        assert!(
            log.contains(&format!("Type `{p}` shadows a Prelude-exposed name")),
            "`{p}` must be [E1004]:\n{log}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}
