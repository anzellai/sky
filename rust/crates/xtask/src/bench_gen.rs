//! `xtask bench-gen` — write the build-speed benchmark app.
//!
//! ```text
//! cargo run -p xtask -- bench-gen --fields N --arms M --modules K --seed S --out DIR
//! ```
//!
//! The generator is `bench_app.rs`; see its docs for the app's shape.

use crate::bench_app::{write, Params};
use std::path::Path;

/// `xtask bench-gen` entry point.
pub fn run(args: &[String]) -> i32 {
    match parse_args(args) {
        Ok((params, out)) => match write(params, Path::new(&out)) {
            Ok(lines) => {
                println!(
                    "bench-gen: wrote {out} (fields {}, arms {}, modules {}, seed {}): {lines} .sky lines",
                    params.fields, params.arms, params.modules, params.seed
                );
                0
            }
            Err(e) => {
                eprintln!("bench-gen: {e}");
                1
            }
        },
        Err(e) => {
            eprintln!("bench-gen: {e}");
            eprintln!("usage: xtask bench-gen --fields N --arms M --modules K --seed S --out DIR");
            2
        }
    }
}

fn parse_args(args: &[String]) -> Result<(Params, String), String> {
    let mut fields = None;
    let mut arms = None;
    let mut modules = None;
    let mut seed = None;
    let mut out = None;
    let mut it = args.iter();
    while let Some(a) = it.next() {
        let (key, inline) = match a.split_once('=') {
            Some((k, v)) => (k, Some(v.to_string())),
            None => (a.as_str(), None),
        };
        let mut value = || -> Result<String, String> {
            match &inline {
                Some(v) => Ok(v.clone()),
                None => it.next().cloned().ok_or(format!("{key} needs a value")),
            }
        };
        let num = |v: String| -> Result<u64, String> {
            v.parse::<u64>()
                .map_err(|_| format!("{key}: `{v}` is not a whole number"))
        };
        match key {
            "--fields" => fields = Some(num(value()?)? as usize),
            "--arms" => arms = Some(num(value()?)? as usize),
            "--modules" => modules = Some(num(value()?)? as usize),
            "--seed" => seed = Some(num(value()?)?),
            "--out" => out = Some(value()?),
            other => return Err(format!("unknown argument `{other}`")),
        }
    }
    let params = Params {
        fields: fields.ok_or("--fields is required")?,
        arms: arms.ok_or("--arms is required")?,
        modules: modules.ok_or("--modules is required")?,
        seed: seed.unwrap_or(1),
    };
    params.validate()?;
    Ok((params, out.ok_or("--out is required")?))
}

#[cfg(test)]
mod tests {
    use crate::bench_app::*;

    fn p(fields: usize, arms: usize, modules: usize, seed: u64) -> Params {
        Params {
            fields,
            arms,
            modules,
            seed,
        }
    }

    #[test]
    fn the_output_is_a_function_of_the_parameters() {
        let a = generate(p(30, 40, 9, 7)).unwrap();
        let b = generate(p(30, 40, 9, 7)).unwrap();
        assert_eq!(a, b);
        let c = generate(p(30, 40, 9, 8)).unwrap();
        assert_ne!(a, c, "the seed must change the app");
    }

    #[test]
    fn it_has_the_requested_size() {
        let files = generate(p(50, 60, 10, 3)).unwrap();
        // K modules: five fixed + the sections.
        let modules = files.keys().filter(|k| k.ends_with(".sky")).count();
        assert_eq!(modules, 10);
        let state = &files["src/State.sky"];
        let model = state.split("type Msg").next().unwrap();
        assert_eq!(model.matches("\n    , f").count(), 50, "{model}");
        let msg = state
            .split("type Msg")
            .nth(1)
            .unwrap()
            .split("init :")
            .next()
            .unwrap();
        // M generated constructors + sign-in + sign-out.
        assert_eq!(msg.matches("\n    | ").count() + 1, 62, "{msg}");
        // Every kind of field and arm is present.
        for t in [
            "String",
            "Int",
            "Maybe String",
            "List Item",
            "Dict String Int",
            "Profile",
            "Bool",
        ] {
            assert!(model.contains(&format!(" : {t}\n")), "no {t} field");
        }
        let main = &files["src/Main.sky"];
        for needle in [
            "Cmd.perform (Persist.countItems ())",
            "(stamp",
            "Ui.onSubmit SubmitSignIn",
            "renameProfile s model.f",
            "Dict.insert s",
        ] {
            assert!(main.contains(needle), "no `{needle}` in Main");
        }
    }

    #[test]
    fn sections_form_a_layered_import_chain() {
        let files = generate(p(20, 20, 8, 1)).unwrap();
        assert!(!files["src/View/Section1.sky"].contains("import View.Section"));
        for j in 2..=3 {
            assert!(files[&format!("src/View/Section{j}.sky")]
                .contains(&format!("import View.Section{} as Prev", j - 1)));
        }
    }

    #[test]
    fn small_inputs_are_refused() {
        assert!(generate(p(6, 20, 8, 1)).is_err());
        assert!(generate(p(20, 11, 8, 1)).is_err());
        assert!(generate(p(20, 20, 5, 1)).is_err());
    }
}
