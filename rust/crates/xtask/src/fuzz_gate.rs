//! `xtask fuzz` — the compiler-robustness + determinism fuzzer (self-host v1 gap:
//! the Haskell oracle has `WellTypedFuzzerSpec` 10k iterations; the Rust rewrite
//! had none). This is an ADDITIVE soundness net — it drives the existing checker /
//! lowerer over a large, mutated input space and asserts two properties. It does
//! NOT change any compiler behaviour.
//!
//! ## Property 1 — ROBUSTNESS (L7: errors are values, never panics)
//! The compiler's contract is that ill-formed input yields DIAGNOSTICS, never a
//! panic. The fuzzer feeds the front half of the pipeline
//! (`syntax::parse` → `hir` resolve → `ty::check_modules`, and `lower` for inputs
//! that typecheck cleanly) a corpus of mostly-INVALID programs and asserts it
//! NEVER panics. Each input runs inside `catch_unwind`, so a panic is recorded
//! (as a REAL BUG) rather than aborting the run.
//!
//! Inputs:
//!   * every valid `.sky` seed (examples `src/` + `sky-stdlib/**`),
//!   * K deterministic byte/token-level MUTANTS per seed (mostly invalid — exactly
//!     what must yield diagnostics, not panics),
//!   * a handful of pure-random ASCII/UTF-8 blobs,
//!   * a committed set of adversarial regression seeds (`tests/fuzz_seeds/*`) so
//!     any panic once found stays locked even if the RNG stops generating it.
//!
//! ## Property 2 — DETERMINISM (L4)
//! For a sample of inputs, the checker (and `lower`, for accepted programs) runs
//! TWICE in the SAME process; the two outputs must be byte-identical (same
//! diagnostic count + same rendered diagnostics/spans, and same lowered Go IR for
//! accepted programs). This complements `xtask repro` (which checks byte-stability
//! of EMITTED Go across fresh PROCESSES for the corpus) by adding determinism over
//! a much wider, mutated input space, in-process.
//!
//! Why an in-process twice-run has teeth against `HashMap`-iteration-order
//! nondeterminism: Rust's `RandomState::new()` derives each `HashMap`'s hasher
//! keys from a per-thread counter that INCREMENTS on every construction — so two
//! `HashMap`s built from the same keys in the same process get DIFFERENT seeds and
//! therefore DIFFERENT iteration orders. If any checker/lowerer code path folds a
//! `HashMap`/`HashSet` iteration into its diagnostics or IR, the two runs diverge
//! here. (Insertion-ordered `IndexMap`/`BTreeMap`/`Vec`/sorted output is immune —
//! which is why the current corpus passes.)
//!
//! Bounded + reproducible: a FIXED seed constant → an identical mutant set every
//! run (so a found bug reproduces), a hard iteration cap, and a wall-clock budget
//! that stops early. The whole run finishes well under the CI budget.

use hir::SourceDb;
use std::panic::{self, AssertUnwindSafe};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

// ---- tuning knobs (all bounded) ------------------------------------------

/// Fixed PRNG seed — identical mutant set every run, so a found bug reproduces.
const FUZZ_SEED: u64 = 0x5169_4C61_695F_5359; // "SkyLai_SY"

/// Debug aid: print every input's descriptor before executing it, so a run killed
/// by an uncatchable failure (stack overflow / OOM) names its last input. Enabled
/// by `SKY_FUZZ_TRACE=1`; off in CI.
fn trace_inputs() -> bool {
    std::env::var("SKY_FUZZ_TRACE")
        .map(|v| v == "1")
        .unwrap_or(false)
}

/// Deterministic mutants generated per valid seed file (Property 1, no-stdlib).
const MUTANTS_PER_SEED: usize = 8;

/// Pure-random ASCII/UTF-8 blobs.
const RANDOM_BLOBS: usize = 48;

/// Hard cap on total inputs exercised in the robustness phase (belt + braces on
/// top of the wall-clock budget).
const MAX_ROBUSTNESS_INPUTS: usize = 20_000;

/// How many valid-corpus files get the (cheap, no-stdlib) determinism run.
const DET_CORPUS_SAMPLE: usize = 200;

/// How many mutants additionally get the determinism run (ill-formed input must
/// also produce a stable diagnostic order run-to-run).
const DET_MUTANT_SAMPLE: usize = 400;

/// Wall-clock budget for the ROBUSTNESS phase (Property 1). After this the gate
/// moves on so the determinism phase always gets to run. This is a SAFETY cap —
/// the intended sample completes well inside it in a normal debug build.
const ROBUSTNESS_BUDGET: Duration = Duration::from_secs(40);

/// Wall-clock budget for the ENTIRE gate. It stops early (reporting how much it
/// exercised) so CI never hangs. Comfortably under the ~90s CI ceiling.
const WALL_BUDGET: Duration = Duration::from_secs(75);

/// Seeds larger than this are skipped as a fuzz base — parsing + checking a 28 KB
/// file costs ~40 ms in a debug build, which starves throughput and adds no
/// coverage a small file doesn't (a 28 KB mutant is not more interesting than a
/// 2 KB one). Small files keep each check in the low-ms range so the mutant count
/// reaches the low thousands within budget. (These files ARE still checked clean
/// with the full stdlib by the `infer`/`roundtrip` gates.)
const MAX_SEED_MUTATE_BYTES: usize = 4_000;

// ---- deterministic PRNG (splitmix64) -------------------------------------

/// A tiny reproducible PRNG. No external dependency (splitmix64), seeded by a
/// fixed constant so runs are identical.
struct SplitMix64(u64);

impl SplitMix64 {
    fn new(seed: u64) -> Self {
        SplitMix64(seed)
    }
    fn next_u64(&mut self) -> u64 {
        self.0 = self.0.wrapping_add(0x9E37_79B9_7F4A_7C15);
        let mut z = self.0;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        z ^ (z >> 31)
    }
    /// Uniform in `[0, n)` (n > 0).
    fn below(&mut self, n: usize) -> usize {
        if n == 0 {
            0
        } else {
            (self.next_u64() % n as u64) as usize
        }
    }
    fn byte(&mut self) -> u8 {
        (self.next_u64() & 0xff) as u8
    }
}

// ---- mutation engine -----------------------------------------------------

/// A snippet library the injector splices in — the tokens most likely to expose a
/// recovery/robustness bug (unbalanced delimiters, keywords in wrong positions).
const INJECT: &[&str] = &[
    "(", ")", "[", "]", "{", "}", "\"", "\\", "->", "|>", "<|", "::", "=", ",", "let", "in",
    "case", "of", "type", "module", "exposing", "import", "if", "then", "else", "\n", "\t", "..",
    "0x", "-", " ",
];

#[derive(Clone, Copy)]
enum Mutation {
    ByteFlip,
    ByteDelete,
    Truncate,
    DupLine,
    DeleteLine,
    SwapTokens,
    Inject,
}

impl Mutation {
    fn name(self) -> &'static str {
        match self {
            Mutation::ByteFlip => "byte-flip",
            Mutation::ByteDelete => "byte-delete",
            Mutation::Truncate => "truncate",
            Mutation::DupLine => "dup-line",
            Mutation::DeleteLine => "delete-line",
            Mutation::SwapTokens => "swap-tokens",
            Mutation::Inject => "inject-token",
        }
    }
    fn pick(rng: &mut SplitMix64) -> Mutation {
        match rng.below(7) {
            0 => Mutation::ByteFlip,
            1 => Mutation::ByteDelete,
            2 => Mutation::Truncate,
            3 => Mutation::DupLine,
            4 => Mutation::DeleteLine,
            5 => Mutation::SwapTokens,
            _ => Mutation::Inject,
        }
    }
}

/// Apply one mutation to `src`. Byte-level mutations can break UTF-8 mid-codepoint;
/// we `from_utf8_lossy` back to a valid `&str` (parse takes `&str`) — the U+FFFD
/// replacements are themselves valid fuzz input.
fn mutate(src: &str, m: Mutation, rng: &mut SplitMix64) -> String {
    match m {
        Mutation::ByteFlip => {
            let mut b = src.as_bytes().to_vec();
            if !b.is_empty() {
                let i = rng.below(b.len());
                b[i] ^= 1 << rng.below(8);
            }
            String::from_utf8_lossy(&b).into_owned()
        }
        Mutation::ByteDelete => {
            let mut b = src.as_bytes().to_vec();
            if !b.is_empty() {
                b.remove(rng.below(b.len()));
            }
            String::from_utf8_lossy(&b).into_owned()
        }
        Mutation::Truncate => {
            let b = src.as_bytes();
            if b.is_empty() {
                return String::new();
            }
            let cut = rng.below(b.len());
            String::from_utf8_lossy(&b[..cut]).into_owned()
        }
        Mutation::DupLine => {
            let lines: Vec<&str> = src.lines().collect();
            if lines.is_empty() {
                return src.to_string();
            }
            let i = rng.below(lines.len());
            let mut out: Vec<&str> = lines.clone();
            out.insert(i, lines[i]);
            out.join("\n")
        }
        Mutation::DeleteLine => {
            let lines: Vec<&str> = src.lines().collect();
            if lines.is_empty() {
                return src.to_string();
            }
            let i = rng.below(lines.len());
            let mut out: Vec<&str> = lines.clone();
            out.remove(i);
            out.join("\n")
        }
        Mutation::SwapTokens => {
            // Split preserving whitespace runs so the reprint stays plausible;
            // swap two adjacent non-space tokens.
            let toks: Vec<&str> = src.split_inclusive(char::is_whitespace).collect();
            if toks.len() < 2 {
                return src.to_string();
            }
            let i = rng.below(toks.len() - 1);
            let mut out: Vec<&str> = toks.clone();
            out.swap(i, i + 1);
            out.concat()
        }
        Mutation::Inject => {
            let b = src.as_bytes();
            let at = if b.is_empty() {
                0
            } else {
                rng.below(b.len() + 1)
            };
            let ins = INJECT[rng.below(INJECT.len())];
            // Snap `at` to a char boundary so slicing is valid.
            let at = floor_char_boundary(src, at);
            let mut out = String::with_capacity(src.len() + ins.len());
            out.push_str(&src[..at]);
            out.push_str(ins);
            out.push_str(&src[at..]);
            out
        }
    }
}

/// Nearest char boundary at or below `idx` (std's `floor_char_boundary` is still
/// nightly-only, so hand-roll it).
fn floor_char_boundary(s: &str, idx: usize) -> usize {
    let mut i = idx.min(s.len());
    while i > 0 && !s.is_char_boundary(i) {
        i -= 1;
    }
    i
}

/// A pseudo-random blob of ASCII-biased bytes (with some high bytes), lossy-decoded
/// to a valid `&str`. Length varies.
fn random_blob(rng: &mut SplitMix64) -> String {
    let len = rng.below(400);
    let mut b = Vec::with_capacity(len);
    for _ in 0..len {
        // Bias toward printable ASCII + newlines, sprinkle high bytes.
        let r = rng.below(100);
        let byte = if r < 80 {
            0x20 + (rng.byte() % 0x5f) // printable ASCII
        } else if r < 90 {
            b"\n\t (){}[]\""
                .iter()
                .copied()
                .nth(rng.below(9))
                .unwrap_or(b' ')
        } else {
            rng.byte()
        };
        b.push(byte);
    }
    String::from_utf8_lossy(&b).into_owned()
}

// ---- the pipeline under test ---------------------------------------------

/// A determinism fingerprint of a checker run: the counts + the rendered
/// diagnostics (code | severity | message | label-spans), and, for an accepted
/// program, the `Debug` of the lowered Go IR items.
#[derive(PartialEq, Eq)]
struct RunFingerprint {
    type_errors: usize,
    name_errors: usize,
    exhaustiveness: usize,
    diags: Vec<String>,
    lowered: Option<String>,
}

fn render_diag(d: &diagnostics::Diagnostic) -> String {
    let mut s = format!("[{}] {:?} {}", d.code.0, d.severity, d.message);
    for l in &d.labels {
        s.push_str(&format!(" @{}..{}", l.span.range.0, l.span.range.1));
    }
    s
}

/// Drive parse → resolve → check (and `lower` when accepted) over `src`, using the
/// provided pre-parsed stdlib (empty slice = no-stdlib fast path). Returns a
/// determinism fingerprint. NEVER guards against panics itself — callers wrap it in
/// `catch_unwind`.
fn run_pipeline(src: &str, stdlib: &[(String, syntax::Parse)]) -> RunFingerprint {
    let mut db = SourceDb::new();
    for (n, parse) in stdlib {
        db.add_module(n, parse.clone());
    }
    let parse = syntax::parse(src, base::FileId(0));
    let mname = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "Main".to_string());
    let parse_errs = parse
        .errors()
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .count()
        + parse.error_node_count();
    let mid = db.add_module(&mname, parse);

    let out = ty::check_modules(&db, &[mid]);

    // Accepted = no parse/name/type error. Only then is lowering meaningful.
    let accepted = parse_errs == 0 && out.name_errors == 0 && out.type_errors == 0;
    let lowered = if accepted {
        let lo = lower::lower_program(&db, mid);
        Some(format!("{:?}", lo.items))
    } else {
        None
    };

    RunFingerprint {
        type_errors: out.type_errors,
        name_errors: out.name_errors,
        exhaustiveness: out.exhaustiveness_warnings,
        diags: out.diagnostics.iter().map(render_diag).collect(),
        lowered,
    }
}

// ---- panic capture -------------------------------------------------------

/// Installs a panic hook that records the message + location of the LAST panic
/// into a shared slot (so the default backtrace print doesn't spam the fuzz log),
/// and restores the previous hook on drop.
struct PanicCapture {
    slot: Arc<Mutex<Option<String>>>,
    prev: Option<Box<dyn Fn(&panic::PanicHookInfo<'_>) + Sync + Send + 'static>>,
}

impl PanicCapture {
    fn install() -> Self {
        let slot: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
        let sink = Arc::clone(&slot);
        let prev = panic::take_hook();
        panic::set_hook(Box::new(move |info| {
            let payload = info
                .payload()
                .downcast_ref::<&str>()
                .map(|s| s.to_string())
                .or_else(|| info.payload().downcast_ref::<String>().cloned())
                .unwrap_or_else(|| "<non-string panic payload>".to_string());
            let loc = info
                .location()
                .map(|l| format!("{}:{}:{}", l.file(), l.line(), l.column()))
                .unwrap_or_else(|| "<unknown location>".to_string());
            *sink.lock().unwrap() = Some(format!("{payload}  (at {loc})"));
        }));
        PanicCapture {
            slot,
            prev: Some(prev),
        }
    }
    /// Take + clear the last recorded panic message.
    fn take(&self) -> Option<String> {
        self.slot.lock().unwrap().take()
    }
}

impl Drop for PanicCapture {
    fn drop(&mut self) {
        if let Some(prev) = self.prev.take() {
            panic::set_hook(prev);
        }
    }
}

// ---- findings ------------------------------------------------------------

struct PanicFinding {
    origin: String,
    mutation: String,
    message: String,
    /// The offending source, truncated for the report.
    snippet: String,
}

struct DetFinding {
    origin: String,
    detail: String,
}

// ---- the gate ------------------------------------------------------------

pub fn run(args: &[String], root: &Path) -> i32 {
    // Debug: run the three front-half stages of ONE file (no stdlib), printing a
    // marker before each so an uncatchable hang/OOM localises to a stage.
    if let Some(f) = args.iter().find_map(|a| a.strip_prefix("--one=")) {
        let with_stdlib = args.iter().any(|a| a == "--stdlib");
        let stdlib = if with_stdlib {
            load_stdlib(&root.join("sky-stdlib"))
        } else {
            Vec::new()
        };
        return debug_one(f, &stdlib);
    }
    let start = Instant::now();
    println!("fuzz gate — compiler-robustness + determinism (L7 + L4)\n");
    println!("seed         = {:#018x} (fixed → reproducible)", FUZZ_SEED);
    println!("mutants/seed = {MUTANTS_PER_SEED}   random-blobs = {RANDOM_BLOBS}");
    println!(
        "wall-budget  = {}s   input-cap = {MAX_ROBUSTNESS_INPUTS}\n",
        WALL_BUDGET.as_secs()
    );

    // ---- load the valid seed corpus (examples/*/src + sky-stdlib/**) ----
    let mut seeds: Vec<(String, String)> = Vec::new(); // (origin, source)
    let examples_root = root.join("examples");
    if let Ok(rd) = std::fs::read_dir(&examples_root) {
        let mut dirs: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
        dirs.sort();
        for d in dirs {
            let src_dir = d.join("src");
            if src_dir.is_dir() {
                collect_sources(&src_dir, root, &mut seeds);
            }
        }
    }
    let stdlib_dir = root.join("sky-stdlib");
    collect_sources(&stdlib_dir, root, &mut seeds);
    seeds.sort();

    // Also pull in the committed regression seeds (locked adversarial inputs).
    let regr_dir = root.join("rust/crates/xtask/tests/fuzz_seeds");
    let mut regression: Vec<(String, String)> = Vec::new();
    collect_sources(&regr_dir, root, &mut regression);
    regression.sort();

    if seeds.is_empty() {
        eprintln!("fuzz: no valid seed .sky files found under examples/*/src + sky-stdlib");
        return 1;
    }

    let capture = PanicCapture::install();
    let mut rng = SplitMix64::new(FUZZ_SEED);

    let mut panics: Vec<PanicFinding> = Vec::new();
    let mut det_findings: Vec<DetFinding> = Vec::new();

    // ==== self-check (G1 evidence): a deliberately-broken input yields a
    // ==== diagnostic (not a panic, not an accept); a trivial valid one is clean.
    // Use an UNRESOLVED-NAME defect so `check_modules` (not just the parser)
    // surfaces the diagnostic — proving the resolver/checker is really exercised.
    let broken = "module Main exposing (main)\nmain = someUndefinedNameXyz";
    let broken_fp = {
        let r = panic::catch_unwind(AssertUnwindSafe(|| run_pipeline(broken, &[])));
        match r {
            Ok(fp) => fp,
            Err(_) => {
                panics.push(PanicFinding {
                    origin: "self-check/broken".into(),
                    mutation: "-".into(),
                    message: capture.take().unwrap_or_default(),
                    snippet: broken.into(),
                });
                RunFingerprint {
                    type_errors: 0,
                    name_errors: 0,
                    exhaustiveness: 0,
                    diags: vec![],
                    lowered: None,
                }
            }
        }
    };
    let broken_diags = broken_fp.diags.len();
    let broken_names = broken_fp.name_errors;
    let trivial_ok = {
        let r = panic::catch_unwind(AssertUnwindSafe(|| {
            run_pipeline("module Main exposing (main)\nmain = 1", &[])
        }));
        matches!(r, Ok(fp) if fp.diags.is_empty())
    };
    println!(
        "self-check: broken input `main = someUndefinedNameXyz` → {broken_diags} diagnostic(s) \
         ({broken_names} name-error, expect >=1); trivial valid input `main = 1` clean = {trivial_ok}"
    );
    use std::io::Write as _;
    let _ = std::io::stdout().flush();

    // ==== Property 1 — ROBUSTNESS (no-stdlib fast path) ====
    // A cheap single-module world keeps each check in the low-ms range so we can
    // exercise thousands of mutants. Panics don't need stdlib present — the parser
    // / resolver / inferrer code paths are exercised on the ill-formed module.
    let mut robustness_inputs = 0usize;
    let mut mutants_exercised = 0usize;
    let mut budget_hit = false;

    // Regression seeds first (always exercised — locked cases).
    for (origin, src) in &regression {
        if over_budget(&start, ROBUSTNESS_BUDGET) {
            budget_hit = true;
            break;
        }
        robustness_inputs += 1;
        exercise_robust(src, &[], origin, "regression", &capture, &mut panics);
    }

    'outer: for (origin, src) in &seeds {
        // Skip oversized seeds entirely — too slow per check in a debug build and
        // no extra coverage (see MAX_SEED_MUTATE_BYTES).
        if src.len() > MAX_SEED_MUTATE_BYTES {
            continue;
        }
        if over_budget(&start, ROBUSTNESS_BUDGET) {
            budget_hit = true;
            break;
        }
        // The valid seed itself (no-stdlib) — exercises the resolver's
        // unresolved-name path on a real program.
        robustness_inputs += 1;
        exercise_robust(src, &[], origin, "seed-verbatim", &capture, &mut panics);

        for _ in 0..MUTANTS_PER_SEED {
            if over_budget(&start, ROBUSTNESS_BUDGET) || robustness_inputs >= MAX_ROBUSTNESS_INPUTS
            {
                budget_hit = over_budget(&start, ROBUSTNESS_BUDGET);
                break 'outer;
            }
            let m = Mutation::pick(&mut rng);
            let mutant = mutate(src, m, &mut rng);
            robustness_inputs += 1;
            mutants_exercised += 1;
            exercise_robust(&mutant, &[], origin, m.name(), &capture, &mut panics);
        }
        if robustness_inputs % 400 == 0 {
            progress(&format!(
                "robustness: {robustness_inputs} inputs, {} panics",
                panics.len()
            ));
        }
    }

    // Pure-random blobs.
    for _ in 0..RANDOM_BLOBS {
        if over_budget(&start, ROBUSTNESS_BUDGET) {
            budget_hit = true;
            break;
        }
        let blob = random_blob(&mut rng);
        robustness_inputs += 1;
        exercise_robust(&blob, &[], "<random-blob>", "random", &capture, &mut panics);
    }
    progress(&format!(
        "robustness phase done: {robustness_inputs} inputs, {} panics",
        panics.len()
    ));

    // ==== Property 2 — DETERMINISM (L4) ====
    // Run the SAME input through the checker (and `lower`, for accepted programs)
    // TWICE in-process and assert byte-identical output. We drive the CHEAP
    // no-stdlib pipeline (each run rebuilds fresh HashMaps → fresh
    // `RandomState` seeds within the process — see the module docs on teeth).
    //
    // Deliberately NOT with the full stdlib loaded: `lower::lower_program` on the
    // eager `SourceDb` re-infers EVERY def in the db, so lowering with all 77
    // stdlib modules present is O(whole-stdlib) per call — a performance cliff,
    // not a correctness path (the real build lowers via `skydb::go_program`,
    // salsa-memoised + DCE-pruned). The emitted-Go determinism of the full corpus
    // is already covered by `xtask repro` across fresh processes; this phase adds
    // in-process CHECKER-diagnostic determinism over the wide mutated space, plus
    // lower-IR determinism on accepted self-contained modules.
    let mut det_runs = 0usize;

    // Self-contained ACCEPTED snippets (no imports → typecheck + LOWER without the
    // stdlib) so the lower-IR determinism comparison has live coverage.
    let accepted_snippets: &[&str] = &[
        "module Main exposing (main)\nmain = 1",
        "module Main exposing (main)\nmain = 1 + 2 * 3",
        "module M exposing (f)\nf a b = a + b\ng = f 1 2",
        "module Main exposing (main)\ntype T = A | B\nmain = A",
        "module Main exposing (main)\nmain =\n    let x = 1\n        y = 2\n    in x + y",
    ];
    for (i, s) in accepted_snippets.iter().enumerate() {
        det_runs += 1;
        exercise_determinism(
            s,
            &format!("<accepted-snippet-{i}>"),
            "accepted",
            &capture,
            &mut det_findings,
            &mut panics,
        );
    }

    // A deterministic sample of the valid corpus (checker determinism) — small
    // files only so each in-process pair stays in the low-ms range.
    let small_seeds: Vec<&(String, String)> = seeds
        .iter()
        .filter(|(_, s)| s.len() <= MAX_SEED_MUTATE_BYTES)
        .collect();
    let corpus_sample: Vec<&&(String, String)> =
        sample(&small_seeds, DET_CORPUS_SAMPLE, FUZZ_SEED ^ 0xABCD);
    progress(&format!(
        "determinism phase: {} accepted snippets + {} corpus + up to {DET_MUTANT_SAMPLE} mutants",
        accepted_snippets.len(),
        corpus_sample.len()
    ));
    for pair in corpus_sample {
        if over_budget(&start, WALL_BUDGET) {
            budget_hit = true;
            break;
        }
        let (origin, src) = &**pair;
        det_runs += 1;
        exercise_determinism(
            src,
            origin,
            "corpus",
            &capture,
            &mut det_findings,
            &mut panics,
        );
    }

    // A deterministic sample of MUTANTS (separate RNG stream) — determinism must
    // hold on ill-formed input too (the checker's error diagnostics must be
    // emitted in a stable order run-to-run).
    let mut mrng = SplitMix64::new(FUZZ_SEED ^ 0x0F0F_0F0F);
    let mut det_mutants = 0usize;
    'det: for (origin, src) in &seeds {
        if src.len() > MAX_SEED_MUTATE_BYTES {
            continue;
        }
        for _ in 0..MUTANTS_PER_SEED {
            if det_mutants >= DET_MUTANT_SAMPLE || over_budget(&start, WALL_BUDGET) {
                budget_hit = budget_hit || over_budget(&start, WALL_BUDGET);
                break 'det;
            }
            let m = Mutation::pick(&mut mrng);
            let mutant = mutate(src, m, &mut mrng);
            det_mutants += 1;
            det_runs += 1;
            exercise_determinism(
                &mutant,
                origin,
                m.name(),
                &capture,
                &mut det_findings,
                &mut panics,
            );
        }
    }
    progress(&format!(
        "determinism phase done: {det_runs} pairs, {} violations",
        det_findings.len()
    ));

    drop(capture); // restore the previous panic hook

    // ---- report ----
    let elapsed = start.elapsed();
    println!();
    println!("{}", "-".repeat(72));
    println!(
        "exercised: {robustness_inputs} robustness inputs ({mutants_exercised} mutants + {RANDOM_BLOBS} blobs + {} regression), \
         {det_runs} determinism pairs ({det_mutants} of them mutants)",
        regression.len()
    );
    println!(
        "wall-clock: {:.1}s{}",
        elapsed.as_secs_f64(),
        if budget_hit {
            "  (budget/cap reached — bounded stop)"
        } else {
            ""
        }
    );
    println!(
        "panics: {}   determinism violations: {}",
        panics.len(),
        det_findings.len()
    );
    println!("{}", "-".repeat(72));

    if !panics.is_empty() {
        println!("\nPANICS ({}):", panics.len());
        for (i, p) in panics.iter().enumerate() {
            println!("  #{i} origin={}  mutation={}", p.origin, p.mutation);
            println!("     panic: {}", p.message);
            println!("     input: {:?}", trunc(&p.snippet, 160));
        }
    }
    if !det_findings.is_empty() {
        println!("\nDETERMINISM VIOLATIONS ({}):", det_findings.len());
        for (i, d) in det_findings.iter().enumerate() {
            println!("  #{i} origin={}\n     {}", d.origin, d.detail);
        }
    }

    if panics.is_empty() && det_findings.is_empty() && broken_diags >= 1 && trivial_ok {
        println!(
            "\nFUZZ GATE: PASS ({robustness_inputs} inputs, {mutants_exercised} mutants, 0 panics, 0 determinism violations)"
        );
        0
    } else {
        if broken_diags < 1 || !trivial_ok {
            println!(
                "\nfuzz: self-check FAILED — the harness is not exercising the checker as expected"
            );
        }
        println!(
            "\nFUZZ GATE: FAIL ({} panic(s), {} determinism violation(s))",
            panics.len(),
            det_findings.len()
        );
        1
    }
}

/// Debug helper (`xtask fuzz --one=FILE`): drive parse → resolve → check on ONE
/// file with a stage marker before each, so an uncatchable hang/OOM localises.
fn debug_one(file: &str, stdlib: &[(String, syntax::Parse)]) -> i32 {
    use std::io::Write as _;
    let src = std::fs::read_to_string(file).unwrap_or_default();
    eprintln!(
        "[stage] parse ({} bytes, stdlib={})…",
        src.len(),
        stdlib.len()
    );
    let _ = std::io::stderr().flush();
    let parse = syntax::parse(&src, base::FileId(0));
    let parse_err_nodes = parse.error_node_count();
    eprintln!(
        "[stage] parse done: {} error nodes, {} diags",
        parse_err_nodes,
        parse.errors().len()
    );
    let _ = std::io::stderr().flush();
    let mut db = SourceDb::new();
    for (n, p) in stdlib {
        db.add_module(n, p.clone());
    }
    let mname = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "Main".to_string());
    let mid = db.add_module(&mname, parse);
    eprintln!("[stage] resolve+check…");
    let _ = std::io::stderr().flush();
    let out = ty::check_modules(&db, &[mid]);
    eprintln!(
        "[stage] check done: {} type / {} name / {} exh",
        out.type_errors, out.name_errors, out.exhaustiveness_warnings
    );
    let _ = std::io::stderr().flush();
    let accepted = out.type_errors == 0 && out.name_errors == 0 && parse_err_nodes == 0;
    eprintln!("[stage] lower (accepted={accepted})…");
    let _ = std::io::stderr().flush();
    let lo = lower::lower_program(&db, mid);
    eprintln!(
        "[stage] lower done: entry_ok={}, {} items, {} errors",
        lo.entry_ok,
        lo.items.len(),
        lo.errors.len()
    );
    0
}

/// Run one robustness input under `catch_unwind`; record a panic finding if it
/// blows up.
fn exercise_robust(
    src: &str,
    stdlib: &[(String, syntax::Parse)],
    origin: &str,
    mutation: &str,
    capture: &PanicCapture,
    panics: &mut Vec<PanicFinding>,
) {
    if trace_inputs() {
        use std::io::Write as _;
        eprintln!("TRACE robust: {origin} [{mutation}] len={}", src.len());
        let _ = std::io::stderr().flush();
        // Dump the exact bytes so an uncatchable failure (OOM/stack overflow) can
        // be reproduced from `/tmp/fuzz_current.sky` after the process is killed.
        let _ = std::fs::write("/tmp/fuzz_current.sky", src);
    }
    let _ = capture.take(); // clear any stale slot
    let r = panic::catch_unwind(AssertUnwindSafe(|| {
        let _ = run_pipeline(src, stdlib);
    }));
    if r.is_err() {
        panics.push(PanicFinding {
            origin: origin.into(),
            mutation: mutation.into(),
            message: capture
                .take()
                .unwrap_or_else(|| "<no message captured>".into()),
            snippet: src.into(),
        });
    }
}

/// Run one input TWICE with stdlib; record a determinism violation if the two
/// fingerprints differ, or a panic finding if either run blows up.
fn exercise_determinism(
    src: &str,
    origin: &str,
    mutation: &str,
    capture: &PanicCapture,
    det: &mut Vec<DetFinding>,
    panics: &mut Vec<PanicFinding>,
) {
    if trace_inputs() {
        use std::io::Write as _;
        eprintln!("TRACE determ: {origin} [{mutation}] len={}", src.len());
        let _ = std::io::stderr().flush();
    }
    let _ = capture.take();
    let run = |s: &str| panic::catch_unwind(AssertUnwindSafe(|| run_pipeline(s, &[])));
    let a = run(src);
    let b = run(src);
    match (a, b) {
        (Ok(fa), Ok(fb)) => {
            if fa != fb {
                det.push(DetFinding {
                    origin: format!("{origin} ({mutation})"),
                    detail: describe_divergence(&fa, &fb),
                });
            }
        }
        _ => {
            panics.push(PanicFinding {
                origin: origin.into(),
                mutation: mutation.into(),
                message: capture
                    .take()
                    .unwrap_or_else(|| "<no message captured>".into()),
                snippet: src.into(),
            });
        }
    }
}

fn describe_divergence(a: &RunFingerprint, b: &RunFingerprint) -> String {
    if a.type_errors != b.type_errors
        || a.name_errors != b.name_errors
        || a.exhaustiveness != b.exhaustiveness
    {
        return format!(
            "counts differ — run1(te={},ne={},ex={}) vs run2(te={},ne={},ex={})",
            a.type_errors,
            a.name_errors,
            a.exhaustiveness,
            b.type_errors,
            b.name_errors,
            b.exhaustiveness
        );
    }
    if a.diags != b.diags {
        // find first differing diagnostic
        for (i, (x, y)) in a.diags.iter().zip(&b.diags).enumerate() {
            if x != y {
                return format!(
                    "diagnostic #{i} differs:\n       run1: {}\n       run2: {}",
                    trunc(x, 90),
                    trunc(y, 90)
                );
            }
        }
        return format!(
            "diagnostic count differs: {} vs {}",
            a.diags.len(),
            b.diags.len()
        );
    }
    if a.lowered != b.lowered {
        return "lowered Go IR differs between the two runs".into();
    }
    "fingerprints differ (unclassified)".into()
}

fn over_budget(start: &Instant, budget: Duration) -> bool {
    start.elapsed() >= budget
}

/// A flushed progress line to stderr, so a bounded/killed run still shows how far
/// it got (and CI logs stay readable).
fn progress(msg: &str) {
    use std::io::Write as _;
    eprintln!("  · {msg}");
    let _ = std::io::stderr().flush();
}

/// A deterministic size-`k` sample of `items` (fixed by `salt`), preserving no
/// particular order but reproducible run-to-run.
fn sample<'a, T>(items: &'a [T], k: usize, salt: u64) -> Vec<&'a T> {
    if items.len() <= k {
        return items.iter().collect();
    }
    let mut rng = SplitMix64::new(salt);
    // stride sampling — reproducible + spread across the corpus
    let mut idxs: Vec<usize> = (0..items.len()).collect();
    // Fisher–Yates prefix of length k.
    for i in 0..k {
        let j = i + rng.below(items.len() - i);
        idxs.swap(i, j);
    }
    idxs[..k].iter().map(|&i| &items[i]).collect()
}

fn trunc(s: &str, n: usize) -> String {
    let t: String = s.chars().take(n).collect();
    if s.chars().count() > n {
        format!("{t}…")
    } else {
        t
    }
}

// ---- source loading ------------------------------------------------------

fn collect_sources(dir: &Path, root: &Path, out: &mut Vec<(String, String)>) {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    for path in files {
        if let Ok(src) = std::fs::read_to_string(&path) {
            let rel = path
                .strip_prefix(root)
                .unwrap_or(&path)
                .to_string_lossy()
                .into_owned();
            out.push((rel, src));
        }
    }
}

/// Parse the stdlib once for the with-stdlib determinism runs (mirrors the naming
/// convention in infer_gate/reject_gate).
fn load_stdlib(dir: &Path) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = module_name(&parse, &path, "sky-stdlib");
        out.push((name, parse));
    }
    out
}

fn module_name(parse: &syntax::Parse, path: &Path, root_marker: &str) -> String {
    if let Some(n) = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
    {
        if !n.is_empty() {
            return n;
        }
    }
    let comps: Vec<&str> = path.iter().filter_map(|c| c.to_str()).collect();
    let start = comps
        .iter()
        .rposition(|c| *c == root_marker)
        .map(|i| i + 1)
        .unwrap_or(0);
    let mut segs: Vec<String> = comps[start..].iter().map(|s| s.to_string()).collect();
    if let Some(last) = segs.last_mut() {
        *last = last.trim_end_matches(".sky").to_string();
    }
    segs.join(".")
}

fn is_generated(path: &Path) -> bool {
    path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out") | Some("sky-out-rust") | Some(".skycache") | Some(".skydeps")
        )
    })
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<PathBuf> = match std::fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok().map(|e| e.path())).collect(),
        Err(_) => return,
    };
    entries.sort();
    for path in entries {
        if is_generated(&path) {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}
