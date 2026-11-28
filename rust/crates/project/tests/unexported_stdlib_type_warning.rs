//! A qualified type under an import of a stdlib module that the module does
//! not export (`Ui.Html` under `import Std.Ui as Ui`) is accepted in v0.32.x,
//! as a nominal type with no module that unifies with ANY type of that name,
//! and `sky check` warns (`hir::ResolveResult::type_warnings`). It becomes
//! `[E1001]` in the next minor (`docs/migration/v0.32.md`).

use project::emit_example_warnings;
use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(dir.pop(), "could not locate repo root");
    }
}

fn warnings_for(tag: &str, import: &str, ty: &str) -> Vec<String> {
    let uniq = format!(
        "sky-unexported-type-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"unexported-type\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    let main = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Ui as Ui exposing (Element)\n\
         {import}\n\
         import Std.Log exposing (println)\n\n\n\
         page : Element msg -> {ty}\n\
         page e =\n    Ui.layout [] e\n\n\n\
         main =\n    println \"ok\"\n"
    );
    std::fs::write(dir.join("src").join("Main.sky"), main).unwrap();
    let out = emit_example_warnings(&repo_root(), &dir);
    let _ = std::fs::remove_dir_all(&dir);
    out.unwrap_or_else(|e| panic!("`{ty}` must build: {e}"))
}

#[test]
fn an_unexported_stdlib_type_warns_and_names_the_exporting_module() {
    let ws = warnings_for("ui-html", "", "Ui.Html msg");
    let want = "`Html` is not exported by `Std.Ui`: use `Html.Html` from `Std.Html`";
    assert_eq!(
        ws.iter().filter(|w| w.contains(want)).count(),
        1,
        "one warning naming {want:?}: {ws:?}"
    );
}

#[test]
fn the_exported_type_gives_no_warning() {
    let ws = warnings_for("html-html", "import Std.Html as Html", "Html.Html msg");
    assert!(
        ws.iter().all(|w| !w.contains("is not exported by")),
        "{ws:?}"
    );
}
