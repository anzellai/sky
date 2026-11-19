//! Sky test coverage: `sky test --coverage[=dir]` and `sky coverage report`.
//!
//! A coverage build lowers with `LowerConfig::coverage`, so the Go-IR carries
//! a marker for every top-level definition and every `case` arm
//! (`lower::coverage`). Codegen renders each marker as nothing and returns the
//! `main.go` position it stood at. The build writes those positions, joined to
//! the Sky module, file, definition and line, as [`MAP_FILE`] beside
//! `main.go`, and builds with [`crate::go_cmd::COVER_FLAGS`]
//! (`go build -cover -coverpkg=sky-app`: the program's own package, never the
//! runtime).
//!
//! Each suite run writes its Go counters to its own `GOCOVERDIR`. A report
//! reads every run in the coverage directory, turns its counters into a
//! profile with `go tool covdata textfmt`, and marks a point hit when the
//! innermost profile block that holds its `main.go` position ran. Runs are
//! merged by Sky identity (module, definition, arm index), never by `main.go`
//! position: two suites emit two different `main.go` files.
//!
//! There are no `//line file.sky:N` directives. The cover tool cannot carry
//! Sky line numbers through them: with a relative file name it takes the file
//! name but keeps the Go line numbers, and with an absolute path it reports
//! `main.go`. A `//line` directive would also remap `go build` errors and
//! panic traces, which the build maps back by `main.go` line.

use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

/// The coverage map a coverage build writes beside `main.go`.
pub const MAP_FILE: &str = "sky-coverage-map.json";

/// `sky test --coverage` with no directory writes here, under the project.
pub const DEFAULT_DIR: &str = ".sky-coverage";

/// The version of [`MAP_FILE`] and of the report JSON.
pub const SCHEMA_VERSION: u64 = 1;

/// What a coverage build adds (`build::build_project_instrumented`).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct CoverageBuild {
    /// `go build -cover` (`sky test --coverage`, `sky fuzz --coverage`).
    pub go_cover: bool,
    /// Each marker also counts in the runtime's in-process bitmap
    /// (`rt.CovHit`), which the guided `sky fuzz` loop reads.
    pub hits: bool,
}

/// Whether the map at `path` asks `go build` for `-cover` (its `goCover`).
/// No map, or one without the field: no.
pub fn map_wants_go_cover(path: &Path) -> bool {
    std::fs::read_to_string(path)
        .ok()
        .and_then(|t| serde_json::from_str::<Value>(&t).ok())
        .is_some_and(|v| v["goCover"] == true)
}

/// Where a module's source comes from. A report shows `App` modules only,
/// unless asked for all.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub enum SourceKind {
    /// The project's own modules (not under `tests/`).
    App,
    /// Test suites and the test runner's generated entry.
    Test,
    /// A Sky dependency (`.skydeps/`, a path dependency).
    Dependency,
    /// The Sky standard library.
    Stdlib,
}

impl SourceKind {
    pub fn as_str(self) -> &'static str {
        match self {
            SourceKind::App => "app",
            SourceKind::Test => "test",
            SourceKind::Dependency => "dependency",
            SourceKind::Stdlib => "stdlib",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        Some(match s {
            "app" => SourceKind::App,
            "test" => SourceKind::Test,
            "dependency" => SourceKind::Dependency,
            "stdlib" => SourceKind::Stdlib,
            _ => return None,
        })
    }
}

/// A point's Sky location, as the build resolves a span.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct SkyLocation {
    /// The display path (`src/Main.sky`), or `<Module/Path>.sky` for the
    /// stdlib.
    pub file: String,
    /// 1-based line.
    pub line: u32,
    /// The byte offset, which orders the arms of a definition.
    pub offset: u32,
    pub source: SourceKind,
}

/// One point of a coverage map.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct MapPoint {
    /// `"function"` or `"arm"`.
    pub arm: bool,
    pub module: String,
    pub file: String,
    pub source: SourceKind,
    pub function: String,
    pub line: u32,
    /// The arm's position among its definition's arms, in source order
    /// (0-based). `None` for a function point.
    pub index: Option<u32>,
    /// Where the point's marker stands in `main.go`: `(line, column)`.
    /// `None` for a definition the program never lowered.
    pub go: Option<(u32, u32)>,
    /// The marker id (`rt.CovHit(<id>)` in a hits build). `None` with `go`.
    pub mark: Option<usize>,
}

/// The coverage map of one build.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct CoverageMap {
    pub points: Vec<MapPoint>,
    /// The build asked for `go build -cover` ([`CoverageBuild::go_cover`]).
    pub go_cover: bool,
}

/// Build a coverage map from a coverage build's points and marker positions.
/// `locate` resolves a span; a point it cannot locate is dropped (it has no
/// file to report under), and so is a stdlib definition that was never
/// lowered.
pub fn build_map(
    points: &[lower::coverage::CovPoint],
    marks: &[skydb::MarkPos],
    locate: &dyn Fn(base::Span) -> Option<SkyLocation>,
) -> CoverageMap {
    let pos: BTreeMap<usize, (u32, u32)> =
        marks.iter().map(|m| (m.mark, (m.line, m.col))).collect();
    // Arm index: the rank of the arm's offset among the distinct arm offsets
    // of its definition. A definition lowered twice (a view-memo copy) records
    // each arm twice, at one offset, so both copies get one index.
    let mut arm_offsets: BTreeMap<(String, String), Vec<u32>> = BTreeMap::new();
    let mut located: Vec<(&lower::coverage::CovPoint, SkyLocation)> = Vec::new();
    for p in points {
        let Some(loc) = p.span.and_then(locate) else {
            continue;
        };
        // A stdlib function the program never lowered is left out: the
        // stdlib has thousands, and none of them can run.
        if p.mark.is_none() && loc.source == SourceKind::Stdlib {
            continue;
        }
        if p.kind == lower::coverage::CovKind::Arm {
            arm_offsets
                .entry((p.module.clone(), p.function.clone()))
                .or_default()
                .push(loc.offset);
        }
        located.push((p, loc));
    }
    for v in arm_offsets.values_mut() {
        v.sort_unstable();
        v.dedup();
    }
    let mut out = Vec::new();
    for (p, loc) in located {
        let arm = p.kind == lower::coverage::CovKind::Arm;
        let index = arm.then(|| {
            arm_offsets[&(p.module.clone(), p.function.clone())]
                .binary_search(&loc.offset)
                .unwrap_or(0) as u32
        });
        out.push(MapPoint {
            arm,
            module: p.module.clone(),
            file: loc.file,
            source: loc.source,
            function: p.function.clone(),
            line: loc.line,
            index,
            go: p.mark.and_then(|m| pos.get(&m).copied()),
            mark: p.mark.filter(|m| pos.contains_key(m)),
        });
    }
    CoverageMap {
        points: out,
        go_cover: false,
    }
}

impl CoverageMap {
    pub fn to_json(&self) -> String {
        let points: Vec<Value> = self
            .points
            .iter()
            .map(|p| {
                json!({
                    "kind": if p.arm { "arm" } else { "function" },
                    "module": p.module,
                    "file": p.file,
                    "source": p.source.as_str(),
                    "function": p.function,
                    "line": p.line,
                    "index": p.index,
                    "go": p.go.map(|(l, c)| json!({ "line": l, "col": c })),
                    "mark": p.mark,
                })
            })
            .collect();
        let v = json!({
            "version": SCHEMA_VERSION,
            "goFile": "main.go",
            "goCover": self.go_cover,
            "points": points,
        });
        serde_json::to_string(&v).unwrap_or_default()
    }

    pub fn from_json(text: &str) -> Result<Self, String> {
        let v: Value = serde_json::from_str(text).map_err(|e| format!("not JSON: {e}"))?;
        if v["version"].as_u64() != Some(SCHEMA_VERSION) {
            return Err(format!(
                "coverage map version {} is not {SCHEMA_VERSION} (rebuild with this sky)",
                v["version"]
            ));
        }
        let mut points = Vec::new();
        for p in v["points"].as_array().ok_or("no `points` array")? {
            let s = |k: &str| p[k].as_str().unwrap_or_default().to_string();
            points.push(MapPoint {
                arm: p["kind"] == "arm",
                module: s("module"),
                file: s("file"),
                source: SourceKind::parse(&s("source")).unwrap_or(SourceKind::App),
                function: s("function"),
                line: p["line"].as_u64().unwrap_or(0) as u32,
                index: p["index"].as_u64().map(|i| i as u32),
                mark: p["mark"].as_u64().map(|m| m as usize),
                go: p["go"].as_object().map(|g| {
                    (
                        g["line"].as_u64().unwrap_or(0) as u32,
                        g["col"].as_u64().unwrap_or(0) as u32,
                    )
                }),
            });
        }
        Ok(CoverageMap {
            points,
            go_cover: v["goCover"] == true,
        })
    }
}

/// One block of a Go coverage profile (`go tool covdata textfmt`).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct ProfileBlock {
    pub start: (u32, u32),
    pub end: (u32, u32),
    pub count: u64,
}

/// The blocks of `main.go` in a text coverage profile. Blocks of every other
/// file (the runtime, if it was ever instrumented) are left out.
pub fn parse_profile(text: &str) -> Vec<ProfileBlock> {
    let mut out = Vec::new();
    for line in text.lines() {
        if line.starts_with("mode:") || line.trim().is_empty() {
            continue;
        }
        // `sky-app/main.go:12.34,15.2 3 1`
        let Some((file, rest)) = line.rsplit_once(':') else {
            continue;
        };
        if !(file == "main.go" || file.ends_with("/main.go")) || file.contains("/rt/") {
            continue;
        }
        let mut parts = rest.split_whitespace();
        let (Some(range), Some(_stmts), Some(count)) = (parts.next(), parts.next(), parts.next())
        else {
            continue;
        };
        let Some((a, b)) = range.split_once(',') else {
            continue;
        };
        let lc = |s: &str| -> Option<(u32, u32)> {
            let (l, c) = s.split_once('.')?;
            Some((l.parse().ok()?, c.parse().ok()?))
        };
        let (Some(start), Some(end), Ok(count)) = (lc(a), lc(b), count.parse()) else {
            continue;
        };
        out.push(ProfileBlock { start, end, count });
    }
    out
}

/// Whether the code at `pos` ran: the innermost block that holds it (the
/// latest start, then the earliest end) has a non-zero count. Blocks of a
/// function literal nest inside the block of the statement that holds it.
pub fn ran_at(blocks: &[ProfileBlock], pos: (u32, u32)) -> bool {
    blocks
        .iter()
        .filter(|b| b.start <= pos && pos <= b.end)
        .max_by(|x, y| x.start.cmp(&y.start).then(y.end.cmp(&x.end)))
        .is_some_and(|b| b.count > 0)
}

// ---- the report -------------------------------------------------------------

#[derive(Clone, PartialEq, Eq, Debug)]
pub struct ArmCov {
    pub index: u32,
    pub line: u32,
    pub hit: bool,
}

#[derive(Clone, PartialEq, Eq, Debug)]
pub struct FunctionCov {
    pub name: String,
    pub line: u32,
    pub hit: bool,
    pub arms: Vec<ArmCov>,
}

#[derive(Clone, PartialEq, Eq, Debug)]
pub struct ModuleCov {
    pub module: String,
    pub file: String,
    pub source: SourceKind,
    pub functions: Vec<FunctionCov>,
}

impl ModuleCov {
    pub fn functions_hit(&self) -> usize {
        self.functions.iter().filter(|f| f.hit).count()
    }
    pub fn arms_total(&self) -> usize {
        self.functions.iter().map(|f| f.arms.len()).sum()
    }
    pub fn arms_hit(&self) -> usize {
        self.functions
            .iter()
            .map(|f| f.arms.iter().filter(|a| a.hit).count())
            .sum()
    }
}

/// A coverage report: modules sorted by name, functions by line, arms by
/// index.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct Report {
    pub modules: Vec<ModuleCov>,
    /// How many suite runs the report merged.
    pub runs: usize,
}

/// Accumulates runs into a [`Report`], merging by Sky identity: a function
/// or arm is hit when any run hit it.
#[derive(Default)]
pub struct Merger {
    // (module) -> (file, source, function -> (line, hit, arm index -> (line, hit)))
    modules: BTreeMap<String, MergedModule>,
    runs: usize,
}

struct MergedModule {
    file: String,
    source: SourceKind,
    functions: BTreeMap<String, MergedFunction>,
}

#[derive(Default)]
struct MergedFunction {
    line: u32,
    hit: bool,
    arms: BTreeMap<u32, (u32, bool)>,
}

impl Merger {
    /// Add one run: its map and its `main.go` profile blocks.
    pub fn add(&mut self, map: &CoverageMap, blocks: &[ProfileBlock]) {
        self.runs += 1;
        for p in &map.points {
            let hit = p.go.is_some_and(|pos| ran_at(blocks, pos));
            let m = self
                .modules
                .entry(p.module.clone())
                .or_insert_with(|| MergedModule {
                    file: p.file.clone(),
                    source: p.source,
                    functions: BTreeMap::new(),
                });
            let f = m.functions.entry(p.function.clone()).or_default();
            match p.index {
                None => {
                    f.line = p.line;
                    f.hit |= hit;
                }
                Some(i) => {
                    let a = f.arms.entry(i).or_insert((p.line, false));
                    a.1 |= hit;
                }
            }
        }
    }

    pub fn finish(self) -> Report {
        let mut modules = Vec::new();
        for (module, m) in self.modules {
            let mut functions: Vec<FunctionCov> = m
                .functions
                .into_iter()
                .map(|(name, f)| FunctionCov {
                    name,
                    // A function seen only through its arms has the line of
                    // its first arm.
                    line: if f.line > 0 {
                        f.line
                    } else {
                        f.arms.values().map(|a| a.0).min().unwrap_or(0)
                    },
                    hit: f.hit || f.arms.values().any(|a| a.1),
                    arms: f
                        .arms
                        .into_iter()
                        .map(|(index, (line, hit))| ArmCov { index, line, hit })
                        .collect(),
                })
                .collect();
            functions.sort_by(|a, b| a.line.cmp(&b.line).then(a.name.cmp(&b.name)));
            modules.push(ModuleCov {
                module,
                file: m.file,
                source: m.source,
                functions,
            });
        }
        Report {
            modules,
            runs: self.runs,
        }
    }
}

impl Report {
    /// The report with only the modules a default report shows: the
    /// project's own (`SourceKind::App`), or every module when `all`.
    pub fn filtered(mut self, all: bool) -> Report {
        if !all {
            self.modules.retain(|m| m.source == SourceKind::App);
        }
        self
    }

    pub fn functions(&self) -> (usize, usize) {
        let hit = self.modules.iter().map(ModuleCov::functions_hit).sum();
        let total = self.modules.iter().map(|m| m.functions.len()).sum();
        (hit, total)
    }

    pub fn arms(&self) -> (usize, usize) {
        let hit = self.modules.iter().map(ModuleCov::arms_hit).sum();
        let total = self.modules.iter().map(ModuleCov::arms_total).sum();
        (hit, total)
    }

    /// The stable JSON shape (`docs/tooling/testing.md`, "Coverage").
    pub fn to_json(&self) -> Value {
        let (fh, ft) = self.functions();
        let (ah, at) = self.arms();
        let modules: Vec<Value> = self
            .modules
            .iter()
            .map(|m| {
                json!({
                    "module": m.module,
                    "file": m.file,
                    "source": m.source.as_str(),
                    "functionsHit": m.functions_hit(),
                    "functionsTotal": m.functions.len(),
                    "armsHit": m.arms_hit(),
                    "armsTotal": m.arms_total(),
                    "functions": m.functions.iter().map(|f| json!({
                        "name": f.name,
                        "line": f.line,
                        "hit": f.hit,
                        "arms": f.arms.iter().map(|a| json!({
                            "index": a.index,
                            "line": a.line,
                            "hit": a.hit,
                        })).collect::<Vec<_>>(),
                    })).collect::<Vec<_>>(),
                })
            })
            .collect();
        json!({
            "version": SCHEMA_VERSION,
            "runs": self.runs,
            "functionsHit": fh,
            "functionsTotal": ft,
            "armsHit": ah,
            "armsTotal": at,
            "modules": modules,
        })
    }

    /// The one-line summary `sky test --coverage` prints.
    pub fn summary_line(&self) -> String {
        let (fh, ft) = self.functions();
        let (ah, at) = self.arms();
        format!(
            "{} module(s), functions {fh}/{ft} ({}), case arms {ah}/{at} ({})",
            self.modules.len(),
            percent(fh, ft),
            percent(ah, at)
        )
    }

    /// The text report. `by_module` prints one line per module; otherwise
    /// each module lists its functions that did not run and its arms that
    /// did not run.
    pub fn to_text(&self, by_module: bool) -> String {
        let mut out = format!("coverage: {}\n", self.summary_line());
        for m in &self.modules {
            out.push_str(&format!(
                "{}  {}  functions {}/{}  arms {}/{}\n",
                m.file,
                m.module,
                m.functions_hit(),
                m.functions.len(),
                m.arms_hit(),
                m.arms_total()
            ));
            if by_module {
                continue;
            }
            for f in &m.functions {
                let missed: Vec<String> = f
                    .arms
                    .iter()
                    .filter(|a| !a.hit)
                    .map(|a| format!("#{} line {}", a.index, a.line))
                    .collect();
                if f.hit && missed.is_empty() {
                    continue;
                }
                let state = if f.hit { "ran" } else { "never ran" };
                out.push_str(&format!("  {}:{} {}  {}", m.file, f.line, f.name, state));
                if !missed.is_empty() {
                    out.push_str(&format!("  arms not run: {}", missed.join(", ")));
                }
                out.push('\n');
            }
        }
        out
    }
}

fn percent(hit: usize, total: usize) -> String {
    if total == 0 {
        "-".to_string()
    } else {
        format!("{:.1}%", hit as f64 * 100.0 / total as f64)
    }
}

/// Arms reached by a guided `sky fuzz` run: `(reached, total)` over the
/// map's `App` arms, leaving out the definitions in `skip` (the harness's own
/// generated functions). `seen` is the marker ids the run counted. An arm a
/// definition carries twice (a view-memo copy) counts once, reached when any
/// copy was.
pub fn arms_reached(
    map: &CoverageMap,
    seen: &std::collections::BTreeSet<usize>,
    skip: &dyn Fn(&str, &str) -> bool,
) -> (usize, usize) {
    let mut arms: BTreeMap<(&str, &str, u32), bool> = BTreeMap::new();
    for p in &map.points {
        let (true, Some(i)) = (p.source == SourceKind::App, p.index) else {
            continue;
        };
        if skip(&p.module, &p.function) {
            continue;
        }
        let hit = p.mark.is_some_and(|m| seen.contains(&m));
        *arms
            .entry((p.module.as_str(), p.function.as_str(), i))
            .or_insert(false) |= hit;
    }
    (arms.values().filter(|h| **h).count(), arms.len())
}

// ---- the coverage directory ---------------------------------------------------

/// The runs directory of a coverage directory.
fn runs_dir(cov_dir: &Path) -> PathBuf {
    cov_dir.join("runs")
}

/// Start a fresh coverage directory: earlier runs are removed, so a report
/// covers the suites of one `sky test --coverage` invocation.
pub fn reset(cov_dir: &Path) -> std::io::Result<()> {
    let runs = runs_dir(cov_dir);
    if runs.exists() {
        std::fs::remove_dir_all(&runs)?;
    }
    std::fs::create_dir_all(&runs)
}

/// A new run directory for one suite: `runs/<n>-<label>/` holding `map.json`
/// and the `covdata/` the suite binary writes its counters to.
pub fn new_run(cov_dir: &Path, label: &str) -> std::io::Result<PathBuf> {
    let runs = runs_dir(cov_dir);
    std::fs::create_dir_all(&runs)?;
    let n = std::fs::read_dir(&runs)?.count();
    let safe: String = label
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '_' })
        .collect();
    let dir = runs.join(format!("{n:03}-{safe}"));
    std::fs::create_dir_all(dir.join("covdata"))?;
    Ok(dir)
}

/// The counters directory of a run (the suite binary's `GOCOVERDIR`).
pub fn run_counters(run: &Path) -> PathBuf {
    run.join("covdata")
}

/// The map file of a run.
pub fn run_map(run: &Path) -> PathBuf {
    run.join("map.json")
}

/// Read every run in `cov_dir` and merge them into one report (unfiltered).
pub fn report(cov_dir: &Path) -> Result<Report, String> {
    let runs = runs_dir(cov_dir);
    let mut dirs: Vec<PathBuf> = std::fs::read_dir(&runs)
        .map_err(|e| {
            format!(
                "no coverage data in {} ({e}); run `sky test --coverage` first",
                cov_dir.display()
            )
        })?
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.is_dir())
        .collect();
    dirs.sort();
    let mut merger = Merger::default();
    for run in &dirs {
        let map_text = match std::fs::read_to_string(run_map(run)) {
            Ok(t) => t,
            // A suite that did not build has no map; nothing ran there.
            Err(_) => continue,
        };
        let map =
            CoverageMap::from_json(&map_text).map_err(|e| format!("{}: {e}", run.display()))?;
        let blocks = profile_blocks(&run_counters(run))?;
        merger.add(&map, &blocks);
    }
    if merger.runs == 0 {
        return Err(format!(
            "no coverage data in {}; run `sky test --coverage` first",
            cov_dir.display()
        ));
    }
    Ok(merger.finish())
}

/// The `main.go` profile blocks of one run's counters, through
/// `go tool covdata textfmt`. A run whose binary wrote no counters (it was
/// killed before it exited) has no blocks: nothing in it counts as run.
fn profile_blocks(counters: &Path) -> Result<Vec<ProfileBlock>, String> {
    let has_data = std::fs::read_dir(counters)
        .map(|d| d.flatten().next().is_some())
        .unwrap_or(false);
    if !has_data {
        return Ok(Vec::new());
    }
    let out_file = counters.with_extension("txt");
    let mut i_arg = std::ffi::OsString::from("-i=");
    i_arg.push(counters.as_os_str());
    let mut o_arg = std::ffi::OsString::from("-o=");
    o_arg.push(out_file.as_os_str());
    let out = std::process::Command::new("go")
        .args(["tool", "covdata", "textfmt"])
        .arg(i_arg)
        .arg(o_arg)
        .output()
        .map_err(|e| format!("could not run `go tool covdata`: {e} (is Go on PATH?)"))?;
    if !out.status.success() {
        return Err(format!(
            "`go tool covdata textfmt` failed on {}: {}",
            counters.display(),
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    let text = std::fs::read_to_string(&out_file).map_err(|e| e.to_string())?;
    let _ = std::fs::remove_file(&out_file);
    Ok(parse_profile(&text))
}

#[cfg(test)]
mod tests {
    use super::*;
    use lower::coverage::{CovKind, CovPoint};

    fn span(off: u32) -> base::Span {
        base::Span::new(base::FileId(0), off, off + 1)
    }

    fn point(mark: Option<usize>, kind: CovKind, function: &str, off: u32) -> CovPoint {
        CovPoint {
            mark,
            kind,
            module: "Main".into(),
            function: function.into(),
            span: Some(span(off)),
        }
    }

    fn locate(sp: base::Span) -> Option<SkyLocation> {
        Some(SkyLocation {
            file: "src/Main.sky".into(),
            line: sp.range.0 / 10 + 1,
            offset: sp.range.0,
            source: SourceKind::App,
        })
    }

    fn mark(mark: usize, line: u32, col: u32) -> skydb::MarkPos {
        skydb::MarkPos { mark, line, col }
    }

    /// `update` (marker 0 at main.go 10.2) with arms at offsets 50 (marker 1,
    /// 11.40) and 30 (marker 2, 12.40), `twice` lowered but never called
    /// (marker 3, 20.2), `unused` never lowered.
    fn sample_map() -> CoverageMap {
        let points = vec![
            point(Some(0), CovKind::Function, "update", 20),
            point(Some(1), CovKind::Arm, "update", 50),
            point(Some(2), CovKind::Arm, "update", 30),
            point(Some(3), CovKind::Function, "twice", 90),
            point(None, CovKind::Function, "unused", 120),
        ];
        let marks = vec![
            mark(0, 10, 2),
            mark(1, 11, 40),
            mark(2, 12, 40),
            mark(3, 20, 2),
        ];
        build_map(&points, &marks, &locate)
    }

    #[test]
    fn the_map_indexes_arms_in_source_order_and_round_trips() {
        let map = sample_map();
        let arms: Vec<(u32, Option<u32>)> = map
            .points
            .iter()
            .filter(|p| p.arm)
            .map(|p| (p.line, p.index))
            .collect();
        // Offset 50 is line 6, the second arm; offset 30 is line 4, the first.
        assert_eq!(arms, vec![(6, Some(1)), (4, Some(0))]);
        let unused = map.points.iter().find(|p| p.function == "unused").unwrap();
        assert_eq!(unused.go, None);
        assert_eq!(CoverageMap::from_json(&map.to_json()).unwrap(), map);
    }

    #[test]
    fn the_profile_parser_keeps_main_go_blocks_only() {
        let text = "mode: set\n\
                    sky-app/main.go:10.2,10.30 1 1\n\
                    sky-app/rt/rt.go:3.1,4.2 1 1\n\
                    sky-app/main.go:11.38,11.60 1 0\n";
        let b = parse_profile(text);
        assert_eq!(b.len(), 2);
        assert_eq!(
            b[1],
            ProfileBlock {
                start: (11, 38),
                end: (11, 60),
                count: 0
            }
        );
    }

    #[test]
    fn the_innermost_block_decides() {
        // An outer statement block that ran holds an inner closure body that
        // did not.
        let blocks = [
            ProfileBlock {
                start: (11, 2),
                end: (11, 90),
                count: 1,
            },
            ProfileBlock {
                start: (11, 38),
                end: (11, 60),
                count: 0,
            },
        ];
        assert!(!ran_at(&blocks, (11, 40)));
        assert!(ran_at(&blocks, (11, 10)));
        assert!(!ran_at(&blocks, (30, 1)), "no block holds it");
    }

    #[test]
    fn the_report_joins_and_merges_runs() {
        let map = sample_map();
        // Run 1: update ran, its arm at 11.40 ran, the one at 12.40 did not.
        let run1 = parse_profile(
            "mode: set\nsky-app/main.go:10.2,10.30 1 1\nsky-app/main.go:11.40,11.60 1 1\n\
             sky-app/main.go:12.40,12.60 1 0\nsky-app/main.go:20.2,21.1 1 0\n",
        );
        let mut m = Merger::default();
        m.add(&map, &run1);
        let r = m.finish();
        let main = &r.modules[0];
        let names: Vec<(&str, bool)> = main
            .functions
            .iter()
            .map(|f| (f.name.as_str(), f.hit))
            .collect();
        assert_eq!(
            names,
            vec![("update", true), ("twice", false), ("unused", false)]
        );
        let update = &main.functions[0];
        assert_eq!(
            update.arms,
            vec![
                ArmCov {
                    index: 0,
                    line: 4,
                    hit: false
                },
                ArmCov {
                    index: 1,
                    line: 6,
                    hit: true
                }
            ]
        );
        assert_eq!(r.functions(), (1, 3));
        assert_eq!(r.arms(), (1, 2));

        // A second run that hits the other arm: merged, both arms ran.
        let run2 = parse_profile("mode: set\nsky-app/main.go:12.40,12.60 1 1\n");
        let mut m = Merger::default();
        m.add(&map, &run1);
        m.add(&map, &run2);
        let r = m.finish();
        assert_eq!(r.arms(), (2, 2));
        assert_eq!(r.runs, 2);

        // The JSON shape is stable.
        let j = r.to_json();
        let f = &j["modules"][0]["functions"][0];
        assert_eq!(j["modules"][0]["module"], "Main");
        assert_eq!(j["modules"][0]["file"], "src/Main.sky");
        assert_eq!(j["modules"][0]["armsHit"], 2);
        assert_eq!(f["name"], "update");
        assert_eq!(f["arms"][1], json!({ "index": 1, "line": 6, "hit": true }));
        let keys: Vec<&String> = j["modules"][0].as_object().unwrap().keys().collect();
        assert_eq!(
            keys,
            [
                "armsHit",
                "armsTotal",
                "file",
                "functions",
                "functionsHit",
                "functionsTotal",
                "module",
                "source"
            ]
        );
    }

    #[test]
    fn a_default_report_shows_the_app_modules_only() {
        let mut map = sample_map();
        map.points[4].module = "Sky.Core.List".into();
        map.points[4].source = SourceKind::Stdlib;
        let mut m = Merger::default();
        m.add(&map, &[]);
        let r = m.finish();
        assert_eq!(r.modules.len(), 2);
        assert_eq!(r.clone().filtered(false).modules.len(), 1);
        assert_eq!(r.filtered(true).modules.len(), 2);
    }
}
