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
//!
//! The generated program puts each stdlib value in a `let`, a record field, a
//! list element and a `Maybe` payload, and, when its type can be written,
//! in an annotated record alias, argument and result (see [`program`]). The
//! hand-written projects under `tests/fixtures/stdlib-type-capture/` cover
//! the rest: case scrutinees, `Result` payloads, a `Codec` round trip, a
//! `Std.Db.Store` row on SQLite, a Sky.Live model served over HTTP, a module
//! boundary, a union nested in records, and parametric project types
//! (`Dict k v`, `Set a`) beside the kernel ones.
//!
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
        if c == '(' || c == '{' || c == '[' {
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
    /// The signature's result type, as written in the stdlib source.
    result: String,
}

/// Calls the generator must not make: the build checks their literal
/// arguments against the program (`Device.provider "a"` needs a server slot
/// `a`). They are reached as references instead.
const NO_CALL: &[(&str, &str)] = &[("Std.Ai.Device", "provider")];

/// For every type name the exported stdlib signatures mention, the exported
/// function with the fewest literal arguments whose result mentions it.
fn plan() -> (BTreeSet<String>, BTreeMap<String, Call>, Declared) {
    let root = repo_root().join("sky-stdlib");
    let mut files = Vec::new();
    sky_files(&root, &mut files);
    let mut all_names = BTreeSet::new();
    let mut best: BTreeMap<String, Call> = BTreeMap::new();
    let mut declared = Declared::default();
    for f in files {
        let src = std::fs::read_to_string(&f).unwrap();
        let module = f
            .strip_prefix(&root)
            .unwrap()
            .with_extension("")
            .to_string_lossy()
            .replace('/', ".");
        let exposed = exposing_list(&src);
        for name in declared_types(&src) {
            if exposed.as_ref().is_none_or(|e| e.contains(&name)) {
                declared
                    .types
                    .entry(name)
                    .or_default()
                    .insert(module.clone());
            }
        }
        declared.imports.insert(
            module.clone(),
            src.lines()
                .filter_map(|l| l.strip_prefix("import "))
                .filter_map(|l| l.split_whitespace().next())
                .map(str::to_string)
                .collect(),
        );
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
                            result: result.clone(),
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
                    result: result.clone(),
                });
            }
        }
    }
    (all_names, best, declared)
}

/// The stdlib's type declarations and imports.
#[derive(Default)]
struct Declared {
    /// For each type name a stdlib module declares and exposes, those modules.
    types: BTreeMap<String, BTreeSet<String>>,
    /// The modules each stdlib module imports.
    imports: BTreeMap<String, BTreeSet<String>>,
}

/// The names of the top-level `type` and `type alias` declarations.
fn declared_types(src: &str) -> Vec<String> {
    src.lines()
        .filter_map(|l| {
            let rest = l
                .strip_prefix("type alias ")
                .or_else(|| l.strip_prefix("type "))?;
            let name: String = rest
                .chars()
                .take_while(|c| c.is_ascii_alphanumeric() || *c == '_')
                .collect();
            name.starts_with(|c: char| c.is_ascii_uppercase())
                .then_some(name)
        })
        .collect()
}

/// The call's result type written so a program can annotate with it: each
/// type name qualified with the alias of the stdlib module that exposes it
/// (the called module's own, else the one module it imports that declares
/// it, else the called module for a kernel type no module declares), each type
/// variable instantiated to `Int`. `None` when the type has a record, a
/// function, a name the source writes qualified, or a name no single
/// importable module exposes.
fn annotatable(
    c: &Call,
    declared: &Declared,
    import: &mut dyn FnMut(&str) -> String,
) -> Option<String> {
    if c.reference || c.result.contains('{') || c.result.contains("->") {
        return None;
    }
    let mut out = String::new();
    let mut word = String::new();
    let flush = |word: &mut String,
                 out: &mut String,
                 import: &mut dyn FnMut(&str) -> String|
     -> Option<()> {
        if word.is_empty() {
            return Some(());
        }
        let w = std::mem::take(word);
        if w.contains('.') {
            return None;
        }
        // A type variable is free in a call with literal arguments (a literal
        // argument is concrete, or `[]` / `Nothing`), so `Int` instantiates it.
        if w.starts_with(|c: char| c.is_ascii_lowercase()) {
            out.push_str("Int");
            return Some(());
        }
        if !w.starts_with(|c: char| c.is_ascii_uppercase()) {
            return None;
        }
        if PROTECTED.contains(&w.as_str()) {
            out.push_str(&w);
            return Some(());
        }
        // The name means the called module's own type, else the type of the
        // one module it imports that declares it, else the kernel type.
        let home = match declared.types.get(&w) {
            None => c.module.clone(),
            Some(ms) if ms.contains(&c.module) => c.module.clone(),
            Some(ms) => {
                let imported: Vec<&String> = ms
                    .iter()
                    .filter(|m| {
                        declared
                            .imports
                            .get(&c.module)
                            .is_some_and(|i| i.contains(*m))
                    })
                    .collect();
                match imported.as_slice() {
                    [m] => (*m).clone(),
                    _ => return None,
                }
            }
        };
        if NOT_IMPORTABLE.contains(&home.as_str()) {
            return None;
        }
        out.push_str(&format!("{}.{w}", import(&home)));
        Some(())
    };
    for ch in c.result.chars() {
        if ch.is_ascii_alphanumeric() || ch == '_' || ch == '.' {
            word.push(ch);
        } else {
            flush(&mut word, &mut out, import)?;
            out.push(ch);
        }
    }
    flush(&mut word, &mut out, import)?;
    Some(out.trim().to_string())
}

#[derive(Clone, Copy, PartialEq)]
enum Variant {
    Control,
    Alias,
    Union,
}

/// The program for one variant, and the line it must print.
///
/// Each stdlib value `v{i}` flows through these positions, beside a value
/// `u{i}` of the project's own type of that name (an `Int` in the control):
///
/// * a `let` binding passed to `toString`,
/// * a field of an unannotated record `{ own = u{i}, kern = v{i} }`,
/// * a list element and a `Maybe` payload (`List.head [ v{i} ]`),
/// * when its result type can be written in an annotation ([`annotatable`]):
///   a field of a record alias `W{i} = { own : <Name>, kern : <its type> }`,
///   the argument and the result of an annotated `pass{i} : W{i} -> W{i}`,
///   and the result of `kern{i} : W{i} -> <its type>`.
///
/// Every position shows one value and adds one to the sum of the project's
/// own values, so the line is `<positions> <positions>` in every variant.
fn program(
    calls: &BTreeMap<String, Call>,
    declared: &Declared,
    variant: Variant,
) -> (String, String) {
    let names: Vec<&String> = calls.keys().collect();
    let mut homes: BTreeSet<String> = calls.values().map(|c| c.module.clone()).collect();
    let annotated: Vec<Option<String>> = names
        .iter()
        .map(|n| {
            annotatable(&calls[*n], declared, &mut |m: &str| {
                homes.insert(m.to_string());
                format!("@{m}@")
            })
        })
        .collect();
    let qual: BTreeMap<String, String> = homes
        .iter()
        .enumerate()
        .map(|(i, m)| (m.clone(), format!("Q{i}")))
        .collect();
    let annotated: Vec<Option<String>> = annotated
        .into_iter()
        .map(|a| {
            a.map(|mut t| {
                for (m, q) in &qual {
                    t = t.replace(&format!("@{m}@"), q);
                }
                t
            })
        })
        .collect();
    let mut s = String::from("module Main exposing (main)\n\n");
    for (m, q) in &qual {
        s.push_str(&format!("import {m} as {q}\n"));
    }
    s.push_str("import Std.Log exposing (println)\n\n\n");
    for (i, n) in names.iter().enumerate() {
        let own_ty = match variant {
            Variant::Control => {
                s.push_str(&format!("u{i} : Int\nu{i} =\n    1\n\n\n"));
                "Int".to_string()
            }
            Variant::Alias => {
                s.push_str(&format!(
                    "type alias {n} =\n    {{ zz : Int }}\n\n\nu{i} : {n}\nu{i} =\n    {{ zz = 1 }}\n\n\n"
                ));
                n.to_string()
            }
            Variant::Union => {
                s.push_str(&format!(
                    "type {n}\n    = {n}C Int\n\n\nu{i} : {n}\nu{i} =\n    {n}C 1\n\n\n\
                     z{i} : {n} -> Int\nz{i} v =\n    case v of\n        {n}C k ->\n            k\n\n\n"
                ));
                n.to_string()
            }
        };
        if let Some(kt) = &annotated[i] {
            s.push_str(&format!(
                "type alias W{i} =\n    {{ own : {own_ty}, kern : {kt} }}\n\n\n\
                 pass{i} : W{i} -> W{i}\npass{i} w =\n    w\n\n\n\
                 kern{i} : W{i} -> {kt}\nkern{i} w =\n    w.kern\n\n\n"
            ));
        }
    }
    let own_of = |i: usize, e: String| match variant {
        Variant::Control => e,
        Variant::Alias => format!("({e}).zz"),
        Variant::Union => format!("z{i} ({e})"),
    };
    let mut shown = Vec::new();
    let mut own = Vec::new();
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
        s.push_str(&format!(
            "        r{i} =\n            {{ own = u{i}, kern = v{i} }}\n\n"
        ));
        shown.push(format!("toString v{i}"));
        own.push(own_of(i, format!("u{i}")));
        shown.push(format!("toString r{i}.kern"));
        own.push(own_of(i, format!("r{i}.own")));
        shown.push(format!(
            "toString (Maybe.withDefault v{i} (List.head [ v{i} ]))"
        ));
        own.push(own_of(
            i,
            format!("Maybe.withDefault u{i} (List.head [ u{i} ])"),
        ));
        if annotated[i].is_some() {
            s.push_str(&format!(
                "        w{i} =\n            pass{i} {{ own = u{i}, kern = v{i} }}\n\n"
            ));
            shown.push(format!("toString (kern{i} w{i})"));
            own.push(own_of(i, format!("(pass{i} w{i}).own")));
        }
    }
    s.push_str("        shown =\n            [ ");
    s.push_str(&shown.join("\n            , "));
    s.push_str("\n            ]\n\n        own =\n            ");
    s.push_str(&own.join("\n                + "));
    s.push_str(
        "\n    in\n    println (String.fromInt (List.length shown) ++ \" \" ++ String.fromInt own)\n",
    );
    (s, format!("{} {}", shown.len(), own.len()))
}

/// One build at a time: the tests of this file run in parallel threads.
static BUILD_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

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
    run_project(tag, &dir)
}

/// Build the project in `dir` and run it; its last output line.
fn run_project(tag: &str, dir: &Path) -> Result<String, String> {
    let _lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (ok, log) = bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(dir),
        Duration::from_secs(900),
    );
    if !ok {
        return Err(format!("{tag}: build failed ({}):\n{log}", dir.display()));
    }
    let (ok, out) = bounded(
        Command::new(dir.join("sky-out/app")).current_dir(dir),
        Duration::from_secs(120),
    );
    if !ok {
        return Err(format!("{tag}: run failed ({}):\n{out}", dir.display()));
    }
    let _ = std::fs::remove_dir_all(dir);
    Ok(out.lines().last().unwrap_or("").to_string())
}

#[test]
fn a_project_type_named_like_any_stdlib_type_never_captures_a_stdlib_value() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let (all_names, mut calls, declared) = plan();
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
    let annotated: Vec<&String> = calls
        .iter()
        .filter(|(_, c)| annotatable(c, &declared, &mut |_| String::new()).is_some())
        .map(|(n, _)| n)
        .collect();
    eprintln!(
        "{} names also flow through an annotated record alias, argument and result",
        annotated.len()
    );
    assert!(
        annotated.iter().any(|n| *n == "Set") && annotated.len() >= 100,
        "the annotated positions must cover Set and most called names: {annotated:?}"
    );
    for variant in [Variant::Control, Variant::Alias, Variant::Union] {
        let (src, expected) = program(&calls, &declared, variant);
        let tag = match variant {
            Variant::Control => "control",
            Variant::Alias => "alias",
            Variant::Union => "union",
        };
        assert_eq!(
            build_and_run(tag, &src),
            Ok(expected),
            "the {tag} program: a stdlib value in every position keeps its type"
        );
    }
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

/// A copy of the fixture project `name` in a fresh scratch directory.
fn fixture(name: &str) -> PathBuf {
    fn copy(from: &Path, to: &Path) {
        std::fs::create_dir_all(to).unwrap();
        for e in std::fs::read_dir(from).unwrap().flatten() {
            let p = e.path();
            if p.is_dir() {
                copy(&p, &to.join(e.file_name()));
            } else {
                std::fs::copy(&p, to.join(e.file_name())).unwrap();
            }
        }
    }
    let dir = scratch(name);
    copy(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("tests/fixtures/stdlib-type-capture")
            .join(name)
            .join("src"),
        &dir.join("src"),
    );
    dir
}

/// Each project declares types named like stdlib types (`Set`, `Dict`,
/// `Array`, `Bytes`, `Store`, `Db`, `Codec`, `Query`) and uses the stdlib
/// values of those types beside its own, in every position a value takes. It
/// is built and run, and prints the line its values compute.
#[test]
fn a_stdlib_value_keeps_its_type_beside_a_same_named_project_type_in_every_position() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let cases: &[(&str, &str)] = &[
        // record fields, annotated arguments and results, a case scrutinee,
        // a `Maybe` and a `Result` payload, list elements
        ("positions", "5 2 x 1 3 7"),
        // the project types from another module, and a `Codec` round trip of
        // a record holding both kinds
        (
            "codec-modules",
            r#"{"own":{"zz":3},"ks":{"a":1},"tags":["x","y"],"arr":[1,2,3],"mine":9,"d":{"dd":"q"}} 18q"#,
        ),
        // `Std.Db.Store` rows on SQLite beside `Store` / `Db` / `Codec` /
        // `Query` / `Set` aliases
        ("store", "2 2 1"),
        // a union from another module nested in records, lists, `Maybe` and
        // a kernel `Dict`
        ("union-modules", "216 dz"),
        // parametric project unions `Dict k v` / `Set a` and a `Bytes` alias
        ("parametric", "17"),
    ];
    let failed: Vec<String> = cases
        .iter()
        .filter_map(|(name, expected)| {
            let got = run_project(name, &fixture(name));
            (got != Ok(expected.to_string()))
                .then(|| format!("fixture {name}: expected {expected:?}, got {got:?}"))
        })
        .collect();
    assert!(failed.is_empty(), "{}", failed.join("\n\n"));
}

/// A Sky.Live app whose model holds project `Set` / `Dict` aliases beside
/// kernel `Set` / `Dict` values. It is built, started, and its page served.
#[test]
fn a_live_model_keeps_stdlib_values_beside_same_named_project_types() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let dir = fixture("live");
    let (ok, log) = bounded(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&dir),
        Duration::from_secs(900),
    );
    assert!(ok, "live: build failed ({}):\n{log}", dir.display());
    let port = std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port();
    let log_path = dir.join("server.log");
    let mut child = Command::new(dir.join("sky-out/app"))
        .current_dir(&dir)
        .env("SKY_LIVE_PORT", port.to_string())
        .stdout(std::fs::File::create(&log_path).unwrap())
        .stderr(std::process::Stdio::null())
        .spawn()
        .expect("spawn the live app");
    let start = Instant::now();
    let mut page = String::new();
    while start.elapsed() < Duration::from_secs(60) {
        page = http_get(port, "/");
        if page.contains("HOMEPAGE") {
            break;
        }
        if child.try_wait().unwrap().is_some() {
            break;
        }
        std::thread::sleep(Duration::from_millis(200));
    }
    let _ = child.kill();
    let _ = child.wait();
    assert!(
        page.starts_with("HTTP/1.1 200") && page.contains("HOMEPAGE 4 3 1 dv"),
        "the page shows the model's values:\n{page}\n{}",
        std::fs::read_to_string(&log_path).unwrap_or_default()
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// One HTTP/1.1 GET to 127.0.0.1:`port`; the whole answer, or "" when the
/// server does not answer.
fn http_get(port: u16, path: &str) -> String {
    use std::io::{Read as _, Write as _};
    let Ok(mut stream) = std::net::TcpStream::connect(("127.0.0.1", port)) else {
        return String::new();
    };
    let _ = stream.set_read_timeout(Some(Duration::from_secs(10)));
    let req = format!("GET {path} HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n");
    if stream.write_all(req.as_bytes()).is_err() {
        return String::new();
    }
    let mut out = String::new();
    let _ = stream.read_to_string(&mut out);
    out
}
