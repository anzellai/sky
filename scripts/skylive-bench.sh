#!/usr/bin/env bash
# Sky.Live per-interaction microbenchmark runner.
#
# Replaces the inferred "a complex view costs 2-10 ms per interaction"
# sizing figure with a measurement, and records the conditions the
# measurement was taken under so the number can never be quoted bare.
#
# Usage:
#   scripts/skylive-bench.sh                 # default: 5 runs, phase-1 only
#   COUNT=9 scripts/skylive-bench.sh         # more repetitions
#   BENCH=BenchmarkDiffOnly scripts/skylive-bench.sh
#   MAX_LOAD=99 scripts/skylive-bench.sh     # override the quiet-machine gate
#
# Output: a directory under docs/perf/runs/<timestamp>/ containing
#   env.txt        machine, commit, load, container flags
#   raw.txt        every repetition, unaggregated
#   summary.tsv    per-benchmark median / min / max / spread
#
# WHY THE LOAD GATE EXISTS
#
# This repo is routinely worked by several agents at once, each running
# cargo/go builds. A benchmark taken at load average 15 on an 8-core
# machine measures scheduler contention, not the code under test -- and
# it does so silently, reporting a confident ns/op that is wrong by a
# factor of three and non-monotonic in input size. The gate refuses to
# produce a summary under those conditions rather than emitting a
# plausible lie.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# `with_timeout <secs> <cmd...>` — the one time bound. See the header of
# scripts/lib/with-timeout.sh for what a bare `timeout` did when it went missing.
source "$ROOT/scripts/lib/with-timeout.sh"
cd "$ROOT"

COUNT="${COUNT:-5}"
BENCH="${BENCH:-BenchmarkInteraction}"
BENCHTIME="${BENCHTIME:-1s}"
# Refuse to summarise when 1-minute load exceeds this. Roughly "at most
# half the cores are busy with someone else's work".
MAX_LOAD="${MAX_LOAD:-4.0}"
TIMEOUT_SECS="${TIMEOUT_SECS:-3600}"

STAMP="$(date +%Y%m%d-%H%M%S)"
OUTDIR="${OUTDIR:-$ROOT/docs/perf/runs/$STAMP}"
mkdir -p "$OUTDIR"

cores="$(sysctl -n hw.ncpu 2>/dev/null || nproc)"
load1="$(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')"

# ---------------------------------------------------------------------
# Conditions block -- written FIRST, so an aborted run still records why
# ---------------------------------------------------------------------
{
  echo "timestamp        $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "commit           $(git rev-parse HEAD 2>/dev/null || echo unknown)"
  echo "branch           $(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
  echo "dirty            $(test -n "$(git status --porcelain 2>/dev/null)" && echo yes || echo no)"
  echo "host             $(hostname)"
  echo "os               $(uname -srm)"
  echo "cpu              $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  echo "cores            $cores"
  echo "load1_at_start   $load1"
  echo "go               $(go version)"
  echo "count            $COUNT"
  echo "benchtime        $BENCHTIME"
  echo "bench            $BENCH"
  # Container flags, when the harness is invoked inside one. CONTAINER_FLAGS
  # is set by the Phase-3 wrapper; empty means a bare host run.
  echo "container_flags  ${CONTAINER_FLAGS:-none (bare host)}"
} > "$OUTDIR/env.txt"

echo "==> conditions recorded in $OUTDIR/env.txt"
cat "$OUTDIR/env.txt"
echo

# ---------------------------------------------------------------------
# Quiet-machine gate
# ---------------------------------------------------------------------
if awk -v l="$load1" -v m="$MAX_LOAD" 'BEGIN{exit !(l > m)}'; then
  echo "REFUSING TO MEASURE: 1-minute load average is $load1 (limit $MAX_LOAD, $cores cores)." >&2
  echo "" >&2
  echo "A benchmark taken now measures contention with whatever else is" >&2
  echo "running, not the Sky.Live diff path. Top consumers:" >&2
  ps -A -o %cpu,comm -r 2>/dev/null | head -6 >&2
  echo "" >&2
  echo "Wait for the machine to go quiet, or set MAX_LOAD=$((${cores}*2)) to" >&2
  echo "override -- in which case the resulting numbers MUST be reported as" >&2
  echo "contended and are not comparable with quiet-machine runs." >&2
  echo "load_gate        FAILED (load $load1 > $MAX_LOAD)" >> "$OUTDIR/env.txt"
  exit 3
fi
echo "load_gate        passed (load $load1 <= $MAX_LOAD)" >> "$OUTDIR/env.txt"

# ---------------------------------------------------------------------
# Non-vacuity gate: the fixtures must still exercise what they claim
# ---------------------------------------------------------------------
echo "==> proving the benchmark fixtures are non-vacuous"
if ! (cd runtime-go && with_timeout 600 go test ./rt \
      -run 'TestBenchFixturesAreNonVacuous|TestBenchTreeSizesMatchReferenceApps' \
      -count 1) ; then
  echo "REFUSING TO MEASURE: fixture gates failed. The benchmark is not" >&2
  echo "exercising the mutation classes it is named for, so its ns/op" >&2
  echo "figures would be meaningless." >&2
  echo "vacuity_gate     FAILED" >> "$OUTDIR/env.txt"
  exit 4
fi
echo "vacuity_gate     passed" >> "$OUTDIR/env.txt"
echo

# ---------------------------------------------------------------------
# Measure
# ---------------------------------------------------------------------
echo "==> running $BENCH x$COUNT (benchtime=$BENCHTIME)"
(cd runtime-go && with_timeout "$TIMEOUT_SECS" go test ./rt \
   -run '^$' -bench "$BENCH" -benchtime "$BENCHTIME" -count "$COUNT" -benchmem) \
  | tee "$OUTDIR/raw.txt"

load_end="$(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')"
echo "load1_at_end     $load_end" >> "$OUTDIR/env.txt"

# ---------------------------------------------------------------------
# Aggregate: median, min, max, spread. NOT a bare mean -- a mean hides
# exactly the run-to-run disagreement that signals a bad measurement.
# ---------------------------------------------------------------------
awk '
  /^Benchmark/ {
    name = $1
    sub(/-[0-9]+$/, "", name)      # strip the -8 GOMAXPROCS suffix
    for (i = 1; i <= NF; i++) if ($i == "ns/op") { v[name][++n[name]] = $(i-1) }
  }
  END {
    printf "%-58s %8s %12s %12s %12s %8s\n", "benchmark", "runs", "median_ns", "min_ns", "max_ns", "spread"
    for (b in v) {
      cnt = n[b]
      # insertion sort
      for (i = 2; i <= cnt; i++) { key = v[b][i]; j = i-1
        while (j > 0 && v[b][j] > key) { v[b][j+1] = v[b][j]; j-- }
        v[b][j+1] = key }
      med = (cnt % 2) ? v[b][(cnt+1)/2] : (v[b][cnt/2] + v[b][cnt/2+1]) / 2
      lo = v[b][1]; hi = v[b][cnt]
      spread = (lo > 0) ? (hi - lo) / lo * 100 : 0
      printf "%-58s %8d %12.0f %12.0f %12.0f %7.1f%%\n", b, cnt, med, lo, hi, spread
    }
  }
' "$OUTDIR/raw.txt" | sort > "$OUTDIR/summary.tsv"

echo
echo "==> summary (median of $COUNT runs)"
cat "$OUTDIR/summary.tsv"

# ---------------------------------------------------------------------
# Variance warning. Do not average away a disagreement -- surface it.
# ---------------------------------------------------------------------
noisy="$(awk 'NR>1 && $NF+0 > 20 {c++} END{print c+0}' "$OUTDIR/summary.tsv")"
total="$(awk 'NR>1 {c++} END{print c+0}' "$OUTDIR/summary.tsv")"
if [ "$noisy" -gt 0 ]; then
  echo
  echo "WARNING: $noisy of $total benchmarks vary by more than 20% between the"
  echo "fastest and slowest run. Those rows are NOT trustworthy to two"
  echo "significant figures; treat them as order-of-magnitude only and"
  echo "re-run on a quieter machine before quoting them."
  echo "variance_warning $noisy/$total benchmarks exceed 20% spread" >> "$OUTDIR/env.txt"
else
  echo "variance_ok      all $total benchmarks within 20% spread" >> "$OUTDIR/env.txt"
fi

echo
echo "==> results in $OUTDIR"
