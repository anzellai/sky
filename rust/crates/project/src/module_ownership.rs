//! Who may define which module name (F-1 / C-17).
//!
//! A project's module world is assembled from four sources: the standard
//! library, the fetched registry packages (`.skydeps/<slug>/src`), the local
//! Sky path dependencies (`sky add ./dir`) and the project's own source roots.
//! Every one is registered BY NAME, and a later registration of a name replaces
//! the earlier file. Before v0.27.0 that let a package ship
//! `src/Sky/Core/Path.sky` whose `safeJoin` allows `../..`, and every
//! `Path.safeJoin` guard in the app silently used it. A dependency named with a
//! bare kernel name (`Auth`, `System`, `Crypto`) hijacked the kernel alias the
//! same way, because a parsed module wins over a kernel pseudo-module
//! (`hir/src/db.rs` `classify_import`).
//!
//! The rules, checked by [`check`] in both loaders (the build and every
//! analysis db), so no path assembles a world the other refuses:
//!
//! - A DEPENDENCY (registry or path) may not define a module under the `Sky.` /
//!   `Std.` roots, a standard-library module, or any name the kernel resolves
//!   (`hir::KERNEL_MODULES`: `Auth`, `System`, `Fmt`, `Context`, ...).
//! - Two dependencies may not define the same module, and one dependency may
//!   not define a module twice.
//! - The project's own modules may not use an exact standard-library module
//!   name. A bare kernel alias stays legal for the app (`module Jobs`,
//!   `module Config` are real app modules), and so does a new name under
//!   `Std.` / `Sky.` that the stdlib does not define: the resolver keeps its
//!   `Std.`-namespace leniency for app modules (`hir/src/resolve.rs`).
//! - The project may not define a module a dependency also defines: which one
//!   the app gets would otherwise depend on load order, silently.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

/// Where a module came from.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub(crate) enum Owner {
    /// A fetched registry package, named by its `[dependencies]` key.
    Registry(String),
    /// A local Sky path dependency, named by its directory.
    PathDep(String),
    /// The project itself.
    App,
}

impl Owner {
    fn describe(&self) -> String {
        match self {
            Owner::Registry(p) => format!("dependency {p}"),
            Owner::PathDep(p) => format!("path dependency {p}"),
            Owner::App => "this project".to_string(),
        }
    }

    fn is_dependency(&self) -> bool {
        !matches!(self, Owner::App)
    }
}

/// True when `name` is reserved for the standard library or the kernel, so a
/// dependency may not define it.
fn reserved_for_dependency(name: &str, stdlib: &BTreeSet<String>) -> bool {
    hir::is_reserved_sky_namespace(name)
        || stdlib.contains(name)
        || hir::KERNEL_MODULES.iter().any(|(k, _)| *k == name)
}

/// The owner of a registry-package file: the `.skydeps/<slug>` directory it
/// sits under, named by the `[dependencies]` key whose slug that is.
pub(crate) fn registry_owner(example_dir: &Path, file: &Path) -> Owner {
    let deps = crate::ffi_ops::read_sky_dependencies(&example_dir.join("sky.toml"));
    let skydeps = example_dir.join(".skydeps");
    let slug = file
        .strip_prefix(&skydeps)
        .ok()
        .and_then(|rel| rel.components().next())
        .map(|c| c.as_os_str().to_string_lossy().into_owned())
        .unwrap_or_else(|| file.display().to_string());
    let name = deps
        .into_iter()
        .map(|(path, _)| path)
        .find(|path| path.replace('/', "_") == slug)
        .unwrap_or(slug);
    Owner::Registry(name)
}

/// The owner of a path-dependency file: the source root it sits under.
pub(crate) fn path_dep_owner(source_dirs: &[PathBuf], file: &Path) -> Owner {
    let dir = source_dirs
        .iter()
        .filter(|d| file.starts_with(d))
        .max_by_key(|d| d.components().count());
    let shown = match dir {
        // `<dep>/src` → `<dep>`: the directory the user named in `sky add`.
        Some(d) => d.parent().unwrap_or(d).display().to_string(),
        None => file.display().to_string(),
    };
    Owner::PathDep(shown)
}

/// Check the module names of one assembled world. `stdlib` holds the
/// standard-library module names; `modules` every non-stdlib module with its
/// owner and file. `Err` carries one line per violation, sorted.
pub(crate) fn check(
    example_dir: &Path,
    stdlib: &BTreeSet<String>,
    modules: &[(String, Owner, PathBuf)],
) -> Result<(), String> {
    let shown = |p: &Path| -> String {
        p.strip_prefix(example_dir)
            .map(|r| r.display().to_string())
            .unwrap_or_else(|_| p.display().to_string())
    };
    let mut errors: BTreeSet<String> = BTreeSet::new();
    let mut by_name: BTreeMap<&str, Vec<(&Owner, &PathBuf)>> = BTreeMap::new();
    for (name, owner, file) in modules {
        by_name.entry(name).or_default().push((owner, file));
        if owner.is_dependency() && reserved_for_dependency(name, stdlib) {
            errors.insert(format!(
                "module {name} in {} ({}) uses a name reserved for the Sky standard library. \
                 A dependency may not define a module under Sky. or Std., a standard-library \
                 module, or a kernel module name such as Auth or System. Rename the module in \
                 the package, or remove the dependency.",
                owner.describe(),
                shown(file)
            ));
        }
        if *owner == Owner::App && stdlib.contains(name) {
            errors.insert(format!(
                "module {name} ({}) has the name of a standard-library module and would \
                 replace it for the whole program. Rename the module.",
                shown(file)
            ));
        }
    }
    for (name, defs) in &by_name {
        if defs.len() < 2 {
            continue;
        }
        let deps: Vec<&(&Owner, &PathBuf)> =
            defs.iter().filter(|(o, _)| o.is_dependency()).collect();
        let app: Vec<&(&Owner, &PathBuf)> =
            defs.iter().filter(|(o, _)| !o.is_dependency()).collect();
        if deps.len() >= 2 {
            let list: Vec<String> = deps
                .iter()
                .map(|(o, f)| format!("{} ({})", o.describe(), shown(f)))
                .collect();
            errors.insert(format!(
                "module {name} is defined more than once by dependencies: {}. \
                 Only one package may define a module.",
                list.join(", ")
            ));
        }
        if !deps.is_empty() && !app.is_empty() {
            errors.insert(format!(
                "module {name} ({}) has the same name as a module of {} ({}). \
                 Which one the program gets would depend on load order. Rename the \
                 project module.",
                shown(app[0].1),
                deps[0].0.describe(),
                shown(deps[0].1)
            ));
        }
    }
    if errors.is_empty() {
        Ok(())
    } else {
        Err(errors.into_iter().collect::<Vec<_>>().join("\n"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn repo_root() -> PathBuf {
        let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !dir.join("sky-stdlib").is_dir() {
            assert!(dir.pop(), "no repo root above the project crate");
        }
        dir
    }

    fn scratch(tag: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "sky-modown-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn write(p: &Path, text: &str) {
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, text).unwrap();
    }

    const MAIN: &str = "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\n\nmain =\n    println \"hi\"\n";

    /// A project with one registry package `github.com/example/evil` and one
    /// local path package `../lib`, each file map written under its source
    /// root. Returns the project dir.
    fn project(
        tag: &str,
        app: &[(&str, &str)],
        reg: &[(&str, &str)],
        path: &[(&str, &str)],
    ) -> PathBuf {
        let base = scratch(tag);
        let dir = base.join("app");
        let mut toml = String::from(
            "name = \"own\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[dependencies]\n",
        );
        if !reg.is_empty() {
            toml.push_str("\"github.com/example/evil\" = \"v1.0.0\"\n");
            for (rel, text) in reg {
                write(
                    &dir.join(".skydeps/github.com_example_evil/src").join(rel),
                    text,
                );
            }
        }
        if !path.is_empty() {
            toml.push_str("\"lib\" = { path = \"../lib\" }\n");
            write(
                &base.join("lib/sky.toml"),
                "name = \"lib\"\nversion = \"0.1.0\"\n\n[lib]\n",
            );
            for (rel, text) in path {
                write(&base.join("lib/src").join(rel), text);
            }
        }
        write(&dir.join("sky.toml"), &toml);
        write(&dir.join("src/Main.sky"), MAIN);
        for (rel, text) in app {
            write(&dir.join("src").join(rel), text);
        }
        dir
    }

    fn load(dir: &Path) -> Result<(), String> {
        crate::build::load_source_db(&repo_root(), dir, None).map(|_| ())
    }

    const EVIL_PATH: &str = "module Sky.Core.Path exposing (safeJoin)\n\n\nsafeJoin : String -> String -> Result String String\nsafeJoin root rel =\n    Ok (root ++ \"/\" ++ rel)\n";

    #[test]
    fn a_registry_package_may_not_define_a_stdlib_module() {
        let dir = project("reg", &[], &[("Sky/Core/Path.sky", EVIL_PATH)], &[]);
        let err = load(&dir).expect_err("a package shadowing Sky.Core.Path must be refused");
        assert!(
            err.contains("module Sky.Core.Path")
                && err.contains("dependency github.com/example/evil")
                && err.contains("reserved for the Sky standard library"),
            "{err}"
        );
    }

    #[test]
    fn a_path_package_may_not_define_a_stdlib_module() {
        let dir = project("path", &[], &[], &[("Sky/Core/Path.sky", EVIL_PATH)]);
        let err = load(&dir).expect_err("a path package shadowing Sky.Core.Path must be refused");
        assert!(
            err.contains("module Sky.Core.Path") && err.contains("path dependency"),
            "{err}"
        );
    }

    #[test]
    fn a_dependency_may_not_take_a_bare_kernel_name() {
        let auth = "module Auth exposing (hashPassword)\n\n\nhashPassword : String -> String\nhashPassword p =\n    p\n";
        let dir = project("kern", &[], &[("Auth.sky", auth)], &[]);
        let err = load(&dir).expect_err("a package named Auth hijacks the kernel alias");
        assert!(err.contains("module Auth"), "{err}");
    }

    #[test]
    fn a_dependency_may_not_add_a_new_std_module() {
        let m = "module Std.Extra exposing (x)\n\n\nx : Int\nx =\n    1\n";
        let dir = project("stdns", &[], &[], &[("Std/Extra.sky", m)]);
        let err = load(&dir).expect_err("the Std. root is reserved for dependencies");
        assert!(err.contains("module Std.Extra"), "{err}");
    }

    #[test]
    fn two_dependencies_may_not_define_one_module() {
        let geo = "module Geo exposing (x)\n\n\nx : Int\nx =\n    1\n";
        let dir = project("dup", &[], &[("Geo.sky", geo)], &[("Geo.sky", geo)]);
        let err = load(&dir).expect_err("two packages defining Geo");
        assert!(
            err.contains("module Geo is defined more than once"),
            "{err}"
        );
    }

    #[test]
    fn the_app_may_not_replace_a_stdlib_module() {
        let log =
            "module Std.Log exposing (println)\n\n\nprintln : String -> ()\nprintln _ =\n    ()\n";
        let dir = project("applog", &[("Std/Log.sky", log)], &[], &[]);
        let err = load(&dir).expect_err("src/Std/Log.sky replaces Std.Log");
        assert!(
            err.contains("module Std.Log") && err.contains("standard-library module"),
            "{err}"
        );
    }

    #[test]
    fn the_app_may_not_shadow_a_dependency_module() {
        let geo = "module Geo exposing (x)\n\n\nx : Int\nx =\n    1\n";
        let dir = project("appdep", &[("Geo.sky", geo)], &[], &[("Geo.sky", geo)]);
        let err = load(&dir).expect_err("the app and a dependency both define Geo");
        assert!(
            err.contains("module Geo") && err.contains("load order"),
            "{err}"
        );
    }

    #[test]
    fn the_app_keeps_bare_kernel_names_and_new_std_names() {
        let jobs = "module Jobs exposing (x)\n\n\nx : Int\nx =\n    1\n";
        let widget = "module Std.Widget exposing (y)\n\n\ny : Int\ny =\n    2\n";
        let geo = "module Geo exposing (z)\n\n\nz : Int\nz =\n    3\n";
        let dir = project(
            "appok",
            &[("Jobs.sky", jobs), ("Std/Widget.sky", widget)],
            &[],
            &[("Geo.sky", geo)],
        );
        load(&dir).expect("app Jobs, app Std.Widget and a dependency Geo are all legal");
    }
}
