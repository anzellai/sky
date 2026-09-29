//! Name the USER's construct when code the Sky.Spa split GENERATED fails to
//! build.
//!
//! The split derives two projects from the app (and, for a `Std.App` entry,
//! first synthesises a `Spa.app` entry). By the time it runs, the app's own
//! source has type-checked, so a type or name error reported from a generated
//! file is a defect of the split, not of the app. The diagnostic the child
//! build prints names a generated file and a generated line, which the user
//! never wrote. Before v0.27.0 the build then said only "the failure above is
//! in the SYNTHESISED client entry".
//!
//! [`explain`] reads those diagnostics back and, for each one:
//!   * finds the top-level definition that encloses the reported line in the
//!     generated file;
//!   * when the app defines the same name in the same module, reports the
//!     app's file and line (exactly the reported line when the definition was
//!     carried over unchanged, else the definition's first line);
//!   * when the name is one the split generated, names the app construct it
//!     was derived from (`Msg`, `Model`, `view`, the routes, …) and where that
//!     construct is.

use std::path::{Path, PathBuf};

/// One diagnostic header the child build printed.
#[derive(Debug, Clone, PartialEq, Eq)]
struct Located {
    /// The location as printed: a path (`src/Main.sky`) or a module name
    /// (`Main`, the analysis' label).
    at: String,
    line: usize,
    col: usize,
    /// The generated tree the diagnostic came from (a leg's project dir, or
    /// the staged synthesised project).
    root: PathBuf,
}

/// Where the split's inputs and outputs live.
pub struct SplitSites<'a> {
    /// The app's own project directory (the one the user edits).
    pub user_project: &'a Path,
    /// The generated project the analysis read (the staged synthesised
    /// project for a `Std.App` entry, else the app's own project).
    pub analysed: &'a Path,
    /// The backend and frontend leg projects (`.split/backend`, `.split/frontend`).
    pub legs: &'a [(&'a str, PathBuf)],
}

/// The explanation lines for the diagnostics in `output` (the text the split
/// or a leg build printed). Empty when `output` holds no located diagnostic.
pub fn explain(output: &str, sites: &SplitSites<'_>) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for loc in located(output, sites) {
        if let Some(line) = explain_one(&loc, sites) {
            if !out.contains(&line) {
                out.push(line);
            }
        }
    }
    out
}

/// The located diagnostic headers in `output`, each tagged with the generated
/// tree it belongs to: a leg's section (`== backend (native) ==`, `== frontend
/// … ==`) when the header sits under one, else the analysed project.
fn located(output: &str, sites: &SplitSites<'_>) -> Vec<Located> {
    let mut root: PathBuf = sites.analysed.to_path_buf();
    let mut out = Vec::new();
    for raw in output.lines() {
        let line = raw.trim();
        if let Some(label) = line.strip_prefix("== ").and_then(|l| l.strip_suffix(" ==")) {
            if let Some((_, dir)) = sites.legs.iter().find(|(half, _)| label.starts_with(half)) {
                root = dir.clone();
            }
            continue;
        }
        if !(line.contains("-- ") && line.contains(" ERROR ")) {
            continue;
        }
        // `… ERROR ---- <at>:<line>:<col> [E2001]`
        let Some(before_code) = line.rsplit_once(" [E").map(|(a, _)| a) else {
            continue;
        };
        let Some(token) = before_code.split_whitespace().last() else {
            continue;
        };
        let mut parts = token.rsplitn(3, ':');
        let (Some(col), Some(ln), Some(at)) = (parts.next(), parts.next(), parts.next()) else {
            continue;
        };
        let (Ok(col), Ok(ln)) = (col.parse::<usize>(), ln.parse::<usize>()) else {
            continue;
        };
        out.push(Located {
            at: at.to_string(),
            line: ln,
            col,
            root: root.clone(),
        });
    }
    out
}

/// The generated file a location names, and its path relative to its
/// project's source root (`Main.sky`, `Page/Home.sky`).
fn generated_file(loc: &Located) -> Option<(PathBuf, PathBuf)> {
    let src_root = loc.root.join(project::configured_source_root(&loc.root));
    let file = if loc.at.ends_with(".sky") {
        let p = Path::new(&loc.at);
        if p.is_absolute() {
            p.to_path_buf()
        } else {
            loc.root.join(p)
        }
    } else {
        src_root.join(format!("{}.sky", loc.at.replace('.', "/")))
    };
    let rel = file.strip_prefix(&src_root).ok()?.to_path_buf();
    file.is_file().then_some((file, rel))
}

/// The top-level definition enclosing 1-based line `line` of `src`: its name
/// and 1-based first line (the annotation, when one precedes the body).
fn enclosing_def(src: &str, line: usize) -> Option<(String, usize)> {
    let lines: Vec<&str> = src.lines().collect();
    let upto = line.min(lines.len());
    for i in (0..upto).rev() {
        let l = lines[i];
        if l.starts_with(' ') || l.starts_with('\t') || l.trim().is_empty() || l.starts_with("--") {
            continue;
        }
        let name = top_level_name(l)?;
        // Step back over the annotation of the same name.
        let mut start = i;
        while start > 0 {
            let prev = lines[start - 1];
            if prev.starts_with(&format!("{name} :")) {
                start -= 1;
                break;
            }
            if prev.trim().is_empty() {
                break;
            }
            start -= 1;
        }
        return Some((name, start + 1));
    }
    None
}

/// The name a top-level line declares (`update msg model =` → `update`,
/// `type alias Model =` → `Model`, `type Msg` → `Msg`).
fn top_level_name(l: &str) -> Option<String> {
    let rest = l
        .strip_prefix("type alias ")
        .or_else(|| l.strip_prefix("type "))
        .unwrap_or(l);
    let name: String = rest
        .chars()
        .take_while(|c| c.is_alphanumeric() || *c == '_' || *c == '\'')
        .collect();
    let first = name.chars().next()?;
    if matches!(name.as_str(), "module" | "import" | "port") || !first.is_alphabetic() {
        return None;
    }
    Some(name)
}

/// The 1-based line where `src` defines `name` at the top level (its
/// annotation when present), and the definition's text block.
fn find_def(src: &str, name: &str) -> Option<(usize, String)> {
    let lines: Vec<&str> = src.lines().collect();
    let decl_starts = |l: &str| {
        top_level_name(l).as_deref() == Some(name)
            && !l.starts_with(' ')
            && (l.starts_with("type ") || l[name.len()..].starts_with([' ', ':']))
    };
    let first = lines.iter().position(|l| decl_starts(l))?;
    Some((first + 1, def_block(&lines, first)))
}

/// The text of the top-level declaration starting at 0-based line `start`:
/// its lines up to (not including) the next top-level line other than the
/// body of an annotation.
fn def_block(lines: &[&str], start: usize) -> String {
    let mut end = start + 1;
    let name = top_level_name(lines[start]).unwrap_or_default();
    while end < lines.len() {
        let l = lines[end];
        let top = !l.is_empty() && !l.starts_with(' ') && !l.starts_with('\t');
        if top && !(top_level_name(l).as_deref() == Some(name.as_str()) && end == start + 1) {
            break;
        }
        end += 1;
    }
    lines[start..end].join("\n").trim_end().to_string()
}

/// What a name the split GENERATED was derived from, as `(user construct,
/// what the generated code does with it)`.
fn generated_origin(name: &str) -> Option<(&'static str, &'static str)> {
    const TABLE: &[(&str, &str, &str)] = &[
        (
            "spaEncodeFollow",
            "Msg",
            "the wire codec for the follow-up Msgs of a server branch",
        ),
        (
            "spaDecodeFollow",
            "Msg",
            "the wire codec for the follow-up Msgs of a server branch",
        ),
        (
            "spaFollow",
            "Msg",
            "the wire record for a follow-up Msg of a server branch",
        ),
        ("spaModel", "Model", "the first-paint model codec"),
        ("spaSsr", "Model", "the server first paint of the model"),
        ("spaInitSeed", "init", "the first-paint seed of the model"),
        ("spaNavSeed", "init", "the first-paint seed of the model"),
        ("spaView", "view", "the client view"),
        ("spaHoist_", "view", "the client view"),
        (
            "spaSubscriptions",
            "subscriptions",
            "the client subscriptions",
        ),
        (
            "spaSubModel",
            "subscriptions",
            "the server subscription check",
        ),
        (
            "spaSubAllowsTopic",
            "subscriptions",
            "the server subscription check",
        ),
        ("spaRoutes", "App.withRoutes", "the client router"),
        ("spaNotFound", "App.withNotFound", "the client router"),
        ("spaOnNavigate", "App.withOnNavigate", "the client router"),
        ("spaHead", "App.withHead", "the page head"),
        ("spaGuard", "App.withGuard", "the guard"),
        ("spaOnRequest", "App.withOnRequest", "the request hook"),
        (
            "spaConsole",
            "App.withConsoleAuth",
            "the console authorisation",
        ),
        ("spaSession", "Model", "the signed session codec"),
        ("verified", "Model", "the signed session check"),
        ("spaRpcError", "App.withOnRpcError", "the RPC error handler"),
        ("spaRunPerform", "update", "a server branch of `update`"),
        ("spaChainSettle", "update", "a server branch of `update`"),
        ("spaMsgArg", "update", "a server branch of `update`"),
        ("spaApiRoutes", "App.withApiRoutes", "the API routes"),
        ("spaBootSetup", "App.withBootSetup", "the boot setup"),
        (
            "blank",
            "Msg",
            "a wire codec derived from a record type the Msgs or the model carry",
        ),
        (
            "auto",
            "Msg",
            "a wire codec derived from a record type the Msgs or the model carry",
        ),
    ];
    TABLE
        .iter()
        .find(|(prefix, _, _)| name.starts_with(prefix))
        .map(|(_, construct, what)| (*construct, *what))
}

/// Find where the app defines `construct` (`Msg`, `update`, …) in its source
/// tree: `(display path, line)`. A builder (`App.withRoutes`) is found by the
/// first line that mentions it.
fn find_construct(user_project: &Path, construct: &str) -> Option<(String, usize)> {
    let src_root = user_project.join(project::configured_source_root(user_project));
    let mut files: Vec<PathBuf> = Vec::new();
    collect_sky(&src_root, &mut files);
    files.sort();
    for f in files {
        let Ok(src) = std::fs::read_to_string(&f) else {
            continue;
        };
        let line = if construct.contains('.') {
            let tail = construct.rsplit('.').next().unwrap_or(construct);
            src.lines()
                .position(|l| l.contains(&format!(".{tail}")) && !l.trim_start().starts_with("--"))
                .map(|i| i + 1)
        } else {
            find_def(&src, construct).map(|(l, _)| l)
        };
        if let Some(line) = line {
            return Some((display(user_project, &f), line));
        }
    }
    None
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for e in rd.flatten() {
        let p = e.path();
        let hidden = p
            .file_name()
            .and_then(|n| n.to_str())
            .is_some_and(|n| n.starts_with('.'));
        if hidden {
            continue;
        }
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|x| x.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn display(project: &Path, file: &Path) -> String {
    file.strip_prefix(project)
        .unwrap_or(file)
        .to_string_lossy()
        .replace('\\', "/")
}

fn explain_one(loc: &Located, sites: &SplitSites<'_>) -> Option<String> {
    let (gen_file, rel) = generated_file(loc)?;
    let gen_src = std::fs::read_to_string(&gen_file).ok()?;
    let (name, gen_start) = enclosing_def(&gen_src, loc.line)?;
    let gen_at = format!("{}:{}:{}", gen_file.display(), loc.line, loc.col);
    // The app's own module at the same path under its source root.
    let user_src_root = sites
        .user_project
        .join(project::configured_source_root(sites.user_project));
    let user_file = user_src_root.join(&rel);
    if let Some(user_src) = std::fs::read_to_string(&user_file).ok() {
        if let Some((user_start, user_block)) = find_def(&user_src, &name) {
            let gen_block = find_def(&gen_src, &name).map(|(_, b)| b);
            let shown = display(sites.user_project, &user_file);
            let verbatim = gen_block.as_deref() == Some(user_block.as_str());
            return Some(if verbatim {
                format!(
                    "  {shown}:{}:{} in your `{name}` (the split's copy of it failed at {gen_at})",
                    user_start + (loc.line - gen_start),
                    loc.col
                )
            } else {
                format!(
                    "  {shown}:{user_start} in your `{name}` (the split rewrote it; the rewritten copy failed at {gen_at})"
                )
            });
        }
    }
    let (construct, what) =
        generated_origin(&name).unwrap_or(("update", "code the split generated for your program"));
    let site = find_construct(sites.user_project, construct)
        .map(|(f, l)| format!(" ({f}:{l})"))
        .unwrap_or_default();
    Some(format!(
        "  `{name}` at {gen_at}: {what}, derived from your `{construct}`{site}"
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!(
            "sky-split-diag-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos())
                .unwrap_or(0)
        ));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(d.join("src")).unwrap();
        d
    }

    const USER: &str = "module Main exposing (main)\n\nimport Std.App as App\n\ntype Msg\n    = Move\n\n\nupdate : Msg -> Int -> ( Int, Cmd Msg )\nupdate msg model =\n    case msg of\n        Move ->\n            ( model + 1, Cmd.none )\n\n\nmain =\n    App.run app\n";

    /// A diagnostic in a definition the split carried over unchanged is
    /// reported at the app's own file and line, even though the generated
    /// copy has more import lines above it (the line numbers differ).
    #[test]
    fn a_carried_over_definition_maps_to_the_users_line() {
        let user = scratch("user");
        let staged = scratch("staged");
        std::fs::write(user.join("src/Main.sky"), USER).unwrap();
        let synth = USER.replace(
            "import Std.App as App\n",
            "import Std.App as App\nimport Std.Spa as Spa\nimport Sky.Core.List as List\n",
        );
        std::fs::write(staged.join("src/Main.sky"), synth).unwrap();
        // `( model + 1, …` is user line 13, staged line 15.
        let output = "sky spa-split: project does not type-check (1 type error(s), 0 name error(s)):\n\n-- TYPE ERROR ------------------ Main:15:15 [E2001]\n[update] type mismatch: Point vs record\n";
        let lines = explain(
            output,
            &SplitSites {
                user_project: &user,
                analysed: &staged,
                legs: &[],
            },
        );
        assert_eq!(lines.len(), 1, "{lines:?}");
        assert!(
            lines[0].contains("src/Main.sky:13:15 in your `update`"),
            "{lines:?}"
        );
        let _ = std::fs::remove_dir_all(&user);
        let _ = std::fs::remove_dir_all(&staged);
    }

    /// A diagnostic in a GENERATED definition of a leg names the app
    /// construct it was derived from and where that construct is.
    #[test]
    fn a_generated_definition_names_the_user_construct() {
        let user = scratch("user2");
        let backend = scratch("backend");
        std::fs::write(user.join("src/Main.sky"), USER).unwrap();
        let generated = format!(
            "{USER}\n\nspaEncodeFollow_ : Msg -> List String\nspaEncodeFollow_ m_ =\n    case m_ of\n        Move ->\n            [ \"Move\", Codec.toJson spaFollowMoveReqCodec {{}} ]\n"
        );
        std::fs::write(backend.join("src/Main.sky"), &generated).unwrap();
        let line = generated
            .lines()
            .position(|l| l.contains("spaFollowMoveReqCodec"))
            .unwrap()
            + 1;
        let output = format!(
            "== backend (native) ==\nsky build: -- TYPE ERROR ---------- src/Main.sky:{line}:26 [E2001]\n[spaEncodeFollow_] type mismatch\n"
        );
        let legs = [("backend", backend.clone())];
        let lines = explain(
            &output,
            &SplitSites {
                user_project: &user,
                analysed: &user,
                legs: &legs,
            },
        );
        assert_eq!(lines.len(), 1, "{lines:?}");
        assert!(
            lines[0].contains("`spaEncodeFollow_`")
                && lines[0].contains("derived from your `Msg` (src/Main.sky:5)"),
            "{lines:?}"
        );
        let _ = std::fs::remove_dir_all(&user);
        let _ = std::fs::remove_dir_all(&backend);
    }

    /// A definition the split REWROTE (a server arm routed to an RPC) is
    /// reported at the app's definition, not at a generated line.
    #[test]
    fn a_rewritten_definition_names_the_users_definition() {
        let user = scratch("user3");
        let frontend = scratch("frontend");
        std::fs::write(user.join("src/Main.sky"), USER).unwrap();
        let rewritten = USER.replace("( model + 1, Cmd.none )", "( model, Spa.rpc \"Move\" )");
        std::fs::write(frontend.join("src/Main.sky"), rewritten).unwrap();
        let output =
            "== frontend (--target web) ==\n-- TYPE ERROR ---- src/Main.sky:13:15 [E2001]\n";
        let legs = [("frontend", frontend.clone())];
        let lines = explain(
            output,
            &SplitSites {
                user_project: &user,
                analysed: &user,
                legs: &legs,
            },
        );
        assert_eq!(lines.len(), 1, "{lines:?}");
        assert!(
            lines[0].contains("src/Main.sky:9 in your `update` (the split rewrote it"),
            "{lines:?}"
        );
        let _ = std::fs::remove_dir_all(&user);
        let _ = std::fs::remove_dir_all(&frontend);
    }

    #[test]
    fn output_without_located_diagnostics_explains_nothing() {
        let d = scratch("none");
        assert!(explain(
            "cannot auto-split: `update` has no top-level `case msg of`",
            &SplitSites {
                user_project: &d,
                analysed: &d,
                legs: &[],
            },
        )
        .is_empty());
        let _ = std::fs::remove_dir_all(&d);
    }
}
