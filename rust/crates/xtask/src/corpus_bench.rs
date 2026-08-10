//! `xtask corpus-bench` — measure `c_measured`, the per-static-case cost
//! (CI/test-architecture v2 §2.1, §3.3, §11-U2).
//!
//! v2 derives the Layer-1 case count rather than choosing it:
//!
//! ```text
//! N_max = (B_L1_seconds × P) / c_measured
//! ```
//!
//! so `c_measured` is the number Phase 4 is built on, and v2 §2.2 makes
//! shrinking the corpus to fit a bad number the forbidden move. This measures it
//! honestly:
//!
//! * at **several corpus sizes**, so the linear model is *fitted* rather than
//!   assumed from two points (the two-point extrapolation is what produced v1's
//!   wrong cost model in the first place);
//! * **repeated**, reporting min / median / max and spread, because a single run
//!   on a laptop is not a measurement;
//! * on **both paths** — the shared world and the whole-program rebuild — so the
//!   speedup is attributable and the full-rebuild fallback rate can be charged at
//!   its real cost, as §1.4(b) requires.
//!
//! The `isolated` column is the per-case cost when a case CANNOT use the
//! prebuilt world (v2 §1.4(b)'s counted `REBUILT` state, and the static-case
//! analogue of §3.3's `N_iso` term). The cost model charges those cases at that
//! rate, so it has to be measured, not assumed equal to the shared rate.

use std::path::Path;
use std::time::Instant;
use ty::shared::SharedWorld;

struct Case {
    modules: Vec<(String, syntax::Parse)>,
    to_check: Vec<String>,
}

pub fn run(args: &[String], root: &Path) -> i32 {
    let sizes: Vec<usize> = args
        .iter()
        .find(|a| a.starts_with("--sizes="))
        .map(|a| {
            a.trim_start_matches("--sizes=")
                .split(',')
                .filter_map(|s| s.trim().parse().ok())
                .collect()
        })
        .unwrap_or_else(|| vec![63, 250, 1000, 2000]);
    let reps: usize = args
        .iter()
        .find(|a| a.starts_with("--reps="))
        .and_then(|a| a.trim_start_matches("--reps=").parse().ok())
        .unwrap_or(5);

    let stdlib = crate::reject_gate::load_dir_pub(&root.join("sky-stdlib"), "sky-stdlib");
    if stdlib.is_empty() {
        eprintln!("corpus-bench: no stdlib under {}/sky-stdlib", root.display());
        return 1;
    }
    let pool = load_pool(root);
    if pool.is_empty() {
        eprintln!("corpus-bench: empty case pool — nothing measured");
        return 1;
    }

    println!("CORPUS BENCH — per-static-case cost (c_measured)");
    println!("  host            : {}", std::env::consts::OS);
    println!("  build           : release");
    println!("  stdlib modules  : {}", stdlib.len());
    println!("  distinct cases  : {} (reject corpus, cycled to reach each size)", pool.len());
    println!("  repetitions     : {reps} per size");
    println!();

    // Base assembly is a once-per-process cost, reported separately so it is
    // never smuggled into (or out of) the per-case number.
    let t = Instant::now();
    let shared = SharedWorld::new(&stdlib);
    let base_ms = t.elapsed().as_secs_f64() * 1000.0;
    println!("  base world assembly (once per process): {base_ms:.0} ms");
    println!();

    println!(
        "{:>7}  {:>10}  {:>10}  {:>10}  {:>8}   {:>12}",
        "N", "shared/ms", "median", "max", "spread", "total/s"
    );
    println!("{}", "-".repeat(70));

    let mut points: Vec<(f64, f64)> = Vec::new(); // (N, total_seconds_median)
    for &n in &sizes {
        let cases = cycle_to(&pool, n);
        let mut per_case: Vec<f64> = Vec::new();
        for _ in 0..reps {
            let t = Instant::now();
            let mut sink = 0usize;
            for c in &cases {
                let r = shared.check_case(&c.modules, &c.to_check);
                sink += r.out.type_errors + r.out.name_errors;
            }
            std::hint::black_box(sink);
            per_case.push(t.elapsed().as_secs_f64() * 1000.0 / n as f64);
        }
        per_case.sort_by(|a, b| a.partial_cmp(b).unwrap());
        let min = per_case[0];
        let med = per_case[per_case.len() / 2];
        let max = per_case[per_case.len() - 1];
        let spread = if min > 0.0 {
            (max - min) / min * 100.0
        } else {
            0.0
        };
        println!(
            "{n:>7}  {min:>10.2}  {med:>10.2}  {max:>10.2}  {spread:>7.1}%   {:>12.2}",
            med * n as f64 / 1000.0
        );
        points.push((n as f64, med * n as f64 / 1000.0));
    }

    // --- fitted linear model: total = intercept + slope * N -----------------
    // Least squares over every measured size, so the model is fitted rather than
    // read off two points.
    let (slope, intercept, r2) = fit(&points);
    println!();
    println!("  FITTED MODEL (least squares over {} sizes):", points.len());
    println!("    total_seconds = {intercept:.3} + {:.5} * N", slope);
    println!("    c_measured    = {:.2} ms/case   (the slope)", slope * 1000.0);
    println!("    R^2           = {r2:.5}");

    // --- the isolated (full-rebuild) rate ----------------------------------
    // Measured at the smallest size only: it is ~25x more expensive per case and
    // measuring it at 2,000 would dominate the run for no extra information.
    let iso_n = sizes.iter().copied().min().unwrap_or(63).min(63);
    let iso_cases = cycle_to(&pool, iso_n);
    let mut iso: Vec<f64> = Vec::new();
    for _ in 0..reps.min(3) {
        let t = Instant::now();
        let mut sink = 0usize;
        for c in &iso_cases {
            // Whole-program: exactly what every gate does today.
            let mut db = hir::SourceDb::new();
            for (n, p) in &stdlib {
                db.add_module(n, p.clone());
            }
            let mut ids = Vec::new();
            for (n, p) in &c.modules {
                let id = db.add_module(n, p.clone());
                if c.to_check.iter().any(|t| t == n) {
                    ids.push(id);
                }
            }
            let out = ty::check_modules(&db, &ids);
            sink += out.type_errors + out.name_errors;
        }
        std::hint::black_box(sink);
        iso.push(t.elapsed().as_secs_f64() * 1000.0 / iso_n as f64);
    }
    iso.sort_by(|a, b| a.partial_cmp(b).unwrap());
    let iso_med = iso[iso.len() / 2];
    println!();
    println!("  ISOLATED (full-rebuild, no shared world), N={iso_n}, {} reps:", iso.len());
    println!("    c_isolated    = {iso_med:.2} ms/case  (min {:.2}, max {:.2})", iso[0], iso[iso.len() - 1]);
    println!("    ratio         = {:.1}x the shared rate", iso_med / (slope * 1000.0));

    // --- the break-even table from v2 §1.3 ---------------------------------
    let c = slope * 1000.0;
    println!();
    println!("  AGAINST v2 §1.3's BREAK-EVEN TABLE");
    println!("{}", "-".repeat(70));
    println!(
        "{:>7}  {:>14}  {:>14}  {:>10}",
        "cases", "1-thread b/e", "4-way b/e", "verdict"
    );
    for (n, be1, be4) in [(1500usize, 80.0, 320.0), (5000usize, 24.0, 96.0)] {
        let v = if c <= be1 {
            "UNDER both"
        } else if c <= be4 {
            "UNDER 4-way"
        } else {
            "OVER"
        };
        println!("{n:>7}  {be1:>11.0} ms  {be4:>11.0} ms  {v:>10}");
    }
    println!();
    println!("  c_measured = {c:.2} ms/case");
    0
}

/// Least squares fit of `total = intercept + slope * N`.
fn fit(points: &[(f64, f64)]) -> (f64, f64, f64) {
    let n = points.len() as f64;
    if n < 2.0 {
        return (0.0, 0.0, 0.0);
    }
    let sx: f64 = points.iter().map(|p| p.0).sum();
    let sy: f64 = points.iter().map(|p| p.1).sum();
    let sxx: f64 = points.iter().map(|p| p.0 * p.0).sum();
    let sxy: f64 = points.iter().map(|p| p.0 * p.1).sum();
    let denom = n * sxx - sx * sx;
    if denom == 0.0 {
        return (0.0, 0.0, 0.0);
    }
    let slope = (n * sxy - sx * sy) / denom;
    let intercept = (sy - slope * sx) / n;
    let mean = sy / n;
    let ss_tot: f64 = points.iter().map(|p| (p.1 - mean).powi(2)).sum();
    let ss_res: f64 = points
        .iter()
        .map(|p| (p.1 - (intercept + slope * p.0)).powi(2))
        .sum();
    let r2 = if ss_tot == 0.0 { 1.0 } else { 1.0 - ss_res / ss_tot };
    (slope, intercept, r2)
}

fn cycle_to(pool: &[Case], n: usize) -> Vec<&Case> {
    (0..n).map(|i| &pool[i % pool.len()]).collect()
}

fn load_pool(root: &Path) -> Vec<Case> {
    let dir = root.join("rust/crates/ty/tests/reject/corpus");
    let mut files: Vec<std::path::PathBuf> = match std::fs::read_dir(&dir) {
        Ok(rd) => rd
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.extension().and_then(|e| e.to_str()) == Some("sky"))
            .collect(),
        Err(_) => return Vec::new(),
    };
    files.sort();
    files
        .iter()
        .map(|f| {
            let src = std::fs::read_to_string(f).unwrap_or_default();
            let parse = syntax::parse(&src, base::FileId(0));
            let mname = parse
                .tree()
                .module_header()
                .and_then(|h| h.name())
                .map(|n| n.text())
                .filter(|s| !s.is_empty())
                .unwrap_or_else(|| "Main".to_string());
            Case {
                modules: vec![(mname.clone(), parse)],
                to_check: vec![mname],
            }
        })
        .collect()
}
