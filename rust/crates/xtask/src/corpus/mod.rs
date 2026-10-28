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
//! xtask corpus --run --shard=I/N  run behavioural slice I of N (interleaved)
//! xtask corpus --isolation     the v2 §3.2 isolation gate (alone / batch / shuffled)
//! xtask corpus --reject        family R — code-pinned rejection + accepted twin
//! xtask corpus --emit-shape    family E — properties of the generated Go, no `go build`
//! xtask corpus --emit-manifest regenerate corpus/manifest.toml
//! ```
//!
//! # The three modes cost three different things
//!
//! `Mode::Behavioural` cases are BUILT AND RUN (`c_u` ≈ 0.70 s each) because
//! their defect class compiles clean and behaves wrong. `Mode::Static` (family
//! R) decides its verdict in-process from `ty::check_modules`; `Mode::EmitShape`
//! (family E) stops at `project::emit_example_source`. Neither pays `go build`,
//! which is why they can be dense where the behavioural families cannot — and
//! why `runner::run_all` selects on the mode rather than running everything the
//! manifest lists.

pub mod axes;
pub mod dict_crossing;
pub mod emit_shape;
pub mod gen;
pub mod isolation;
pub mod lang_shapes;
pub mod manifest;
pub mod reject_matrix;
pub mod runner;
pub mod stdlib;
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

/// Write every generated case's Sky source under `dir`, one project per case.
///
/// A debugging aid, not a gate: the corpus checks in a manifest rather than
/// thousands of `.sky` files (see `manifest.rs`), which is right for review and
/// unhelpful the moment a generator edit produces a syntax error. This turns
/// "run 320 builds and read the tail" into "look at the file".
fn dump_sources(root: &Path, dir: &Path) -> i32 {
    let _ = root;
    let cases = all_cases();
    for c in &cases {
        let base = dir.join(c.id.replace('/', "__")).join("src");
        for (name, src) in &c.modules {
            let p = base.join(name.replace('.', "/")).with_extension("sky");
            if let Some(parent) = p.parent() {
                if let Err(e) = std::fs::create_dir_all(parent) {
                    eprintln!("corpus: mkdir {}: {e}", parent.display());
                    return 1;
                }
            }
            if let Err(e) = std::fs::write(&p, src) {
                eprintln!("corpus: write {}: {e}", p.display());
                return 1;
            }
        }
    }
    println!(
        "wrote {} case project(s) under {}",
        cases.len(),
        dir.display()
    );
    0
}

pub fn run(args: &[String], root: &Path) -> i32 {
    if let Some(a) = args.iter().find(|a| a.starts_with("--dump-sources=")) {
        let dir = a.split_once('=').map(|(_, v)| v).unwrap_or_default();
        return dump_sources(root, Path::new(dir));
    }
    if args.iter().any(|a| a == "--prove-isolation-needed") {
        return isolation::prove_isolation_needed(root);
    }
    if args.iter().any(|a| a == "--stdlib-coverage") {
        return stdlib::report(root);
    }
    if args.iter().any(|a| a == "--isolation") {
        return isolation::run(root);
    }
    if args.iter().any(|a| a == "--witness") {
        return witness::run(root);
    }
    if args.iter().any(|a| a == "--reject") {
        return reject_matrix::run(root);
    }
    if args.iter().any(|a| a == "--emit-shape") {
        return emit_shape::run(root);
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
        return match parse_shard(args) {
            Err(e) => {
                eprintln!("{e}");
                2
            }
            Ok(None) => runner::run_all(root),
            Ok(Some((i, n))) => runner::run_shard(root, i, n).0,
        };
    }
    eprintln!(
        "usage: xtask corpus [--spike[=N] | --run [--shard=I/N] | --isolation | --witness \
         | --reject | --emit-shape | --emit-manifest | --check-manifest \
         | --stdlib-coverage | --dump-sources=DIR]"
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
    // Families R and E. Their strata live in their own modules rather than in
    // `axes::STRATA`, because `STRATA` is the list of full-cross strata the
    // VALUE-asserting families share and `witness.rs` requires an
    // `axis_under_test` entry for every member. R's witness is its accepted twin
    // and E's is the property itself, so neither belongs in that table — but both
    // belong in the manifest, which is the single membership authority (v2 §3.1).
    out.extend(reject_matrix::all());
    out.extend(emit_shape::all());
    out
}

/// The cases that are BUILT AND RUN. `runner` selects on this rather than on
/// `all_cases()`: a family-R program is ill-typed by construction and a family-E
/// case's verdict is a property of the emitted Go, so running either through the
/// behavioural path would spend `c_u` to learn nothing.
pub fn behavioural_cases() -> Vec<gen::GenCase> {
    all_cases()
        .into_iter()
        .filter(|c| c.mode == gen::Mode::Behavioural)
        .collect()
}

/// `N_min`, printed rather than chosen (v2 §2.1).
pub fn n_min() -> usize {
    all_cases().len()
}

/// How many shards the behavioural corpus runs in. One registered harness gate
/// per shard (`corpus-1` … `corpus-4`, `harness/registry.rs`), so each shard
/// keeps its own exact assertion count, budget and falsifier proof.
///
/// Why sharded: the behavioural cases are built AND run, so the cost is
/// `cases × c_u` and grows with the corpus. On 2026-10-14 the single `corpus`
/// gate reached about case 350 of 554 when the harness killed it at its 4000 s
/// budget on a CI runner (about 11.4 s of wall clock per case, Go build cache
/// restored). A runner's cores bound the per-case `go build`, so the only lever
/// that adds hardware is more runners. The v0.27.9 build-speed work then cut
/// the per-case cost to about 0.8 s (530 cases in 422 s, release run
/// 38922351710), so the four shards share one CI job today, each under a
/// 1200 s budget. The shards stay: they keep each slice's count, budget and
/// proof separate, and a slow slice moves to its own runner with no new code.
pub const SHARDS: usize = 4;

/// The number of cases shard `i` of `n` holds when the corpus has `total`.
///
/// `const` so the registry can declare each shard's EXACT assertion count from
/// `CORPUS_BEHAVIOURAL_EXPECTED` at compile time. It must agree with
/// [`shard_slice`]; `shard_len_matches_the_slice` asserts that.
pub const fn shard_len(total: usize, i: usize, n: usize) -> usize {
    total / n + if i < total % n { 1 } else { 0 }
}

/// Keep slice `i` of `n`, interleaved by index (`index % n == i`).
///
/// The same design as `xtask repro --shard=I/N`: interleaving (stride `n`)
/// rather than a contiguous split spreads every stratum across every shard, so
/// each shard holds part of each family and the shards finish in roughly equal
/// wall clock. The union of shards `0 … n-1` is the whole input and the shards
/// are pairwise disjoint (unit-tested below), so every case runs exactly once
/// across the fan-out.
pub fn shard_slice<T>(items: Vec<T>, i: usize, n: usize) -> Vec<T> {
    assert!(n > 0 && i < n, "corpus shard {i}/{n}: requires 0 <= I < N");
    items
        .into_iter()
        .enumerate()
        .filter(|(idx, _)| idx % n == i)
        .map(|(_, c)| c)
        .collect()
}

/// The behavioural cases of shard `i` of `n`.
pub fn behavioural_shard(i: usize, n: usize) -> Vec<gen::GenCase> {
    shard_slice(behavioural_cases(), i, n)
}

/// Parse `--shard=I/N`. `Ok(None)` when absent; `Err` for a malformed or
/// out-of-range spec, because a shard that silently runs nothing (or the wrong
/// slice) is how a fan-out reports PASS over a corpus it never ran.
fn parse_shard(args: &[String]) -> Result<Option<(usize, usize)>, String> {
    let Some(spec) = args.iter().find_map(|a| a.strip_prefix("--shard=")) else {
        return Ok(None);
    };
    let (i, n) = spec
        .split_once('/')
        .ok_or_else(|| format!("corpus: --shard expects I/N (e.g. 0/4), got {spec:?}"))?;
    let i: usize = i
        .trim()
        .parse()
        .map_err(|_| format!("corpus: --shard index is not a number: {i:?}"))?;
    let n: usize = n
        .trim()
        .parse()
        .map_err(|_| format!("corpus: --shard count is not a number: {n:?}"))?;
    if n == 0 || i >= n {
        return Err(format!(
            "corpus: --shard=I/N requires 0 <= I < N (got {i}/{n})"
        ));
    }
    Ok(Some((i, n)))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ids(total: usize) -> Vec<usize> {
        (0..total).collect()
    }

    const SIZES: [usize; 11] = [0, 1, 3, 4, 5, 7, 138, 554, 555, 700, 1001];

    /// For every corpus size and every shard count, the union of all shards is
    /// the whole corpus and no case is in two shards. A break here means a
    /// fan-out that drops cases (green over a corpus it never ran) or runs a
    /// case twice.
    #[test]
    fn shards_are_disjoint_and_total() {
        for total in SIZES {
            for n in 1..=6 {
                let mut union: Vec<usize> = Vec::new();
                for i in 0..n {
                    union.extend(shard_slice(ids(total), i, n));
                }
                let before = union.len();
                union.sort_unstable();
                union.dedup();
                assert_eq!(
                    before,
                    union.len(),
                    "total={total} n={n}: a case is in two shards"
                );
                assert_eq!(
                    union,
                    ids(total),
                    "total={total} n={n}: the union is not the corpus"
                );
            }
        }
    }

    /// The `const` count the registry declares per shard agrees with the slice
    /// the runner actually takes, and the sizes stay within one of each other.
    #[test]
    fn shard_len_matches_the_slice() {
        for total in SIZES {
            for n in 1..=6 {
                let lens: Vec<usize> = (0..n)
                    .map(|i| shard_slice(ids(total), i, n).len())
                    .collect();
                for (i, l) in lens.iter().enumerate() {
                    assert_eq!(shard_len(total, i, n), *l, "total={total} shard {i}/{n}");
                }
                let lo = *lens.iter().min().unwrap();
                let hi = *lens.iter().max().unwrap();
                assert!(
                    hi - lo <= 1,
                    "total={total} n={n}: shard sizes {lens:?} differ by more than one"
                );
            }
        }
    }

    /// Each shard's falsifiers (`harness/registry.rs`) corrupt the expectation
    /// of a WHOLE family: `record_update` and Family S (`stdlib_edge`). That
    /// proves a shard red only if the shard holds at least one case of each.
    /// Interleaving guarantees it while each family has `SHARDS` or more
    /// consecutive cases; this asserts it on the real corpus, so a reordering
    /// that strands a family in some shards fails here, not as a VACUOUS proof
    /// in a release run.
    #[test]
    fn every_shard_holds_the_families_its_falsifiers_corrupt() {
        for i in 0..SHARDS {
            let shard = behavioural_shard(i, SHARDS);
            for stratum in ["record_update", "stdlib_edge"] {
                assert!(
                    shard.iter().any(|c| c.stratum == stratum),
                    "corpus shard {i}/{SHARDS} holds no `{stratum}` case, so its \
                     falsifier for that family cannot make it red"
                );
            }
        }
    }

    #[test]
    fn parse_shard_reads_i_of_n_and_refuses_bad_specs() {
        let a = |s: &str| vec!["--run".to_string(), s.to_string()];
        assert_eq!(parse_shard(&a("--shard=0/4")), Ok(Some((0, 4))));
        assert_eq!(parse_shard(&a("--shard=3/4")), Ok(Some((3, 4))));
        assert_eq!(parse_shard(&["--run".to_string()]), Ok(None));
        for bad in ["--shard=4/4", "--shard=0/0", "--shard=2", "--shard=x/4"] {
            assert!(parse_shard(&a(bad)).is_err(), "{bad} must be refused");
        }
    }

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
            assert!(
                ids.contains(&pin_id),
                "pinned coordinate {pin_id} is not in the corpus"
            );
            for n in pin.neighbourhood(s.axes) {
                // An inadmissible neighbour is not a gap: it is a point where
                // the generator has nothing it can independently predict, and
                // `axes::admissible` says so by name. Requiring it here would
                // force a vacuous case into existence to satisfy a count.
                if !axes::admissible(s.name, &n) {
                    continue;
                }
                let nid = format!("{}/{}", s.name, n.slug());
                assert!(
                    ids.contains(&nid),
                    "distance-1 neighbour {nid} of {pin_id} is not in the corpus"
                );
            }
        }
    }
}
