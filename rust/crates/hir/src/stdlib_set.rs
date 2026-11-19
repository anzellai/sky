//! The standard-library module set: the modules of `sky-stdlib/`, by origin.
//!
//! A module is the stdlib when it IS one of the files the compiler embeds,
//! never because its name starts with `Std.` or `Sky.`. A project may define a
//! new module under those roots (`module Sky.Pages`); it is app code, and the
//! checker must check it like app code. The project may not reuse an exact
//! stdlib name, and a dependency may not use the roots at all
//! (`project/src/module_ownership.rs`), so a registered module whose name is
//! in this set was loaded from the stdlib.

include!(concat!(env!("OUT_DIR"), "/stdlib_modules.rs"));

/// Is `name` a module of the standard library (a file of `sky-stdlib/`)?
pub fn is_stdlib_module_name(name: &str) -> bool {
    STDLIB_MODULES.binary_search(&name).is_ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_set_is_sorted_and_names_real_stdlib_modules() {
        assert!(STDLIB_MODULES.windows(2).all(|w| w[0] < w[1]));
        assert!(is_stdlib_module_name("Std.App"));
        assert!(is_stdlib_module_name("Sky.Core.Basics"));
        assert!(!is_stdlib_module_name("Sky.Pages"));
        assert!(!is_stdlib_module_name("Std.Pages"));
        assert!(!is_stdlib_module_name("Std"));
    }
}
