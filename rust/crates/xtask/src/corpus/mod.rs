//! `xtask corpus` — Layer 1, the combinatorial corpus (CI/test-architecture v2 §3).
//!
//! The mandate's layer 1: *"Not 'more examples': systematic VARIATION. Every
//! shipped defect in this repo's history was ordinary usage in a combination
//! nobody had tried."* The axes are mined from that history (`axes.rs`), the
//! expected values are constructed by the generator rather than observed from
//! the compiler (`gen.rs`), and membership is declared in a checked-in manifest
//! rather than discovered by `read_dir` (`manifest.rs`).
//!
//! Subcommands:
//!
//! ```text
//! xtask corpus --spike[=N]     the v2 §2.3 / §3.5 red-rate spike
//! xtask corpus --run           run the whole corpus
//! xtask corpus --isolation     the v2 §3.2 isolation gate (alone / batch / shuffled)
//! xtask corpus --emit-manifest regenerate corpus/manifest.toml
//! ```

pub mod axes;
pub mod gen;
pub mod isolation;
pub mod manifest;
pub mod runner;
pub mod witness;

use std::path::Path;

/// A deterministic seed derived from the commit sha.
///
/// Sampled gates (isolation, witness) use this so their sample **rotates**
/// across commits. A fixed sample would only ever prove the same handful of
/// cases, and the ones it never picked would be permanently unverified while the
/// gate reported green.
pub fn commit_seed(root: &Path) -> usize {
    std::process::Command::new("git")
        .args(["rev-parse", "HEAD"])
        .current_dir(root)
        .output()
        .ok()
        .and_then(|o| String::from_utf8(o.stdout).ok())
        .map(|s| {
            s.trim()
                .bytes()
                .fold(0usize, |a, b| a.wrapping_mul(31).wrapping_add(b as usize))
        })
        .unwrap_or(0)
}

pub fn run(args: &[String], root: &Path) -> i32 {
    if args.iter().any(|a| a == "--prove-isolation-needed") {
        return isolation::prove_isolation_needed(root);
    }
    if args.iter().any(|a| a == "--isolation") {
        return isolation::run(root);
    }
    if args.iter().any(|a| a == "--witness") {
        return witness::run(root);
    }
    if args.iter().any(|a| a == "--emit-manifest") {
        return manifest::emit(root);
    }
    if args.iter().any(|a| a == "--check-manifest") {
        return manifest::check(root);
    }
    if let Some(spike) = args.iter().find(|a| a.starts_with("--spike")) {
        let n: usize = spike
            .split_once('=')
            .and_then(|(_, v)| v.parse().ok())
            .unwrap_or(100);
        return runner::spike(root, n);
    }
    if args.iter().any(|a| a == "--run") {
        return runner::run_all(root);
    }
    eprintln!(
        "usage: xtask corpus [--spike[=N] | --run | --isolation | --witness \
         | --emit-manifest | --check-manifest]"
    );
    2
}

/// Every case the generator produces: for each stratum, the full cross of its
/// axes.
///
/// v2 §2.1 computes `N_min` from the coverage guarantee rather than choosing it:
/// the full-cross strata + the distance-1 neighbourhood of every pinned
/// coordinate + the pinned coordinates themselves. Because each stratum here IS
/// a full cross of the axes its defect moved along, the neighbourhood of its
/// pinned coordinate is a subset of that cross — so the cross is the binding
/// term and `N_min` is its total.
pub fn all_cases() -> Vec<gen::GenCase> {
    let mut out = Vec::new();
    for s in axes::STRATA {
        for a in axes::full_cross(s) {
            out.push(gen::build(s, &a));
        }
    }
    out
}

/// `N_min`, printed rather than chosen (v2 §2.1).
pub fn n_min() -> usize {
    all_cases().len()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Every pinned coordinate's distance-1 neighbourhood is inside the corpus.
    /// This is the mandate's *"its NEIGHBOURS in the variation space become cases
    /// too — because the bug was never unique, only the combination was"*, made
    /// checkable.
    #[test]
    fn every_pinned_neighbourhood_is_covered() {
        let ids: std::collections::BTreeSet<String> =
            all_cases().into_iter().map(|c| c.id).collect();
        for s in axes::STRATA {
            let pin = axes::pinned_coordinate(s.name).unwrap();
            let pin_id = format!("{}/{}", s.name, pin.slug());
            assert!(ids.contains(&pin_id), "pinned coordinate {pin_id} is not in the corpus");
            for n in pin.neighbourhood(s.axes) {
                let nid = format!("{}/{}", s.name, n.slug());
                assert!(
                    ids.contains(&nid),
                    "distance-1 neighbour {nid} of {pin_id} is not in the corpus"
                );
            }
        }
    }
}
