#!/usr/bin/env bash
#
# scale-projection.sh — Experiment #5 from the v0.16.2 composite-
# test-apps RFC.
#
# Establishes the slope of (wall time + cache growth) vs. (synthetic
# fixture count). Lets us predict when the test design breaks again.
#
# Generates N synthetic Sky projects under /tmp/sky-scale-proj/<N>/,
# each one `main = println "ok-<N>"` — minimal but real `sky build`
# invocations. Then runs them in a loop, measuring per-fixture
# wall + cache delta. Output is a CSV-like row in the RUN_LOG so
# you can plot the slope.
#
# Per CLAUDE.md §6: don't run with N > 100 unless you have ≥ 30 GB
# disk free (each synth project's sky-out/ adds ~50 MB).
#
# Usage:
#   ./docs/v0.16.x-console/composite-test-experiments/scale-projection.sh
#   N=50 ./docs/v0.16.x-console/composite-test-experiments/scale-projection.sh
#
# Optional env:
#   N=<int>   — number of synthetic fixtures (default 20)
#   SKIP_BUILD=1 — skip the actual sky-build (dry-run for harness dev)

set -euo pipefail

N="${N:-20}"
EXP_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${EXP_DIR}/../../.." && pwd)"
RUN_LOG="${EXP_DIR}/scale-projection-RUN_LOG.md"

cd "${REPO_ROOT}"

# Disk floor scaled to N.
disk_free_kb=$(df -k . | tail -1 | awk '{print $4}')
need_kb=$(( N * 50 * 1024 ))  # ~50 MB per fixture
need_gb=$(( need_kb / 1024 / 1024 ))
have_gb=$(( disk_free_kb / 1024 / 1024 ))
if (( need_kb > disk_free_kb )); then
    echo "[scale-projection] ✘ ABORT: N=${N} needs ~${need_gb} GB; only ${have_gb} GB free." >&2
    exit 2
fi

sky=./sky-out/sky
if [[ ! -x "${sky}" ]]; then
    echo "[scale-projection] ✘ ABORT: ./sky-out/sky not built. Run \`cabal install\` first." >&2
    exit 3
fi

sky_commit=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
cpu_brand=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo "unknown")

SCRATCH=$(mktemp -d "${TMPDIR:-/tmp}"/sky-scale-proj.XXX)
trap 'rm -rf "${SCRATCH}"' EXIT INT TERM

echo "[scale-projection] sky commit ${sky_commit} on ${cpu_brand}, N=${N}"
echo "[scale-projection] scratch: ${SCRATCH}"

# Use a fresh isolated GOCACHE so prior runs don't bias.
ISO_CACHE=$(mktemp -d "${TMPDIR:-/tmp}"/sky-scale-gocache.XXX)
trap 'rm -rf "${SCRATCH}" "${ISO_CACHE}"' EXIT INT TERM
export GOCACHE="${ISO_CACHE}"

# ─── Generate fixtures ─────────────────────────────────────────────

for i in $(seq -f "%03g" 1 "${N}"); do
    proj="${SCRATCH}/proj-${i}"
    mkdir -p "${proj}/src"
    cat > "${proj}/sky.toml" <<EOF
name = "scale-proj-${i}"
version = "0.0.1"
entry = "src/Main.sky"
EOF
    cat > "${proj}/src/Main.sky" <<EOF
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)
main = println "ok-${i}"
EOF
done

# ─── Build them in a loop, sampling wall + cache every N/10 ────────

epoch_start=$(date +%s)
sample_interval=$(( N / 10 ))
(( sample_interval < 1 )) && sample_interval=1

samples_csv=""

for idx in $(seq 1 "${N}"); do
    i=$(printf "%03d" "${idx}")
    proj="${SCRATCH}/proj-${i}"

    if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
        ( cd "${proj}" && "${REPO_ROOT}/${sky}" build src/Main.sky ) > /dev/null 2>&1 || true
    fi

    # Sample every Nth.
    if (( idx % sample_interval == 0 )) || (( idx == N )); then
        now=$(date +%s)
        elapsed=$(( now - epoch_start ))
        cache_kb=$(du -sk "${ISO_CACHE}" 2>/dev/null | awk '{print $1}')
        cache_kb=${cache_kb:-0}
        cache_mb=$(( cache_kb / 1024 ))
        samples_csv="${samples_csv}${idx}:${elapsed}s:${cache_mb}MB "
        echo "[scale-projection] ${idx}/${N} — ${elapsed}s wall, ${cache_mb} MB GOCACHE"
    fi
done

epoch_end=$(date +%s)
wall_s=$(( epoch_end - epoch_start ))

cache_kb=$(du -sk "${ISO_CACHE}" 2>/dev/null | awk '{print $1}')
cache_mb=$(( ${cache_kb:-0} / 1024 ))

# ─── Report ────────────────────────────────────────────────────────

per_fixture_ms=$(( wall_s * 1000 / N ))
cache_per_fixture_mb=$(awk -v c="${cache_mb}" -v n="${N}" 'BEGIN{printf "%.1f", c/n}')

if [[ ! -f "${RUN_LOG}" ]]; then
    {
        echo "# Scale-projection — Experiment #5"
        echo ""
        echo "Each row: N synthetic Sky fixtures (each \`main = println \"ok\"\`)"
        echo "built sequentially against a fresh isolated GOCACHE."
        echo "Use the per-fixture rates + intermediate samples to plot slope."
        echo ""
        echo "| Date (UTC) | sky commit | CPU | N | Wall (s) | ms/fixture | Cache (MB) | MB/fixture | Samples |"
        echo "|---|---|---|---|---|---|---|---|---|"
    } > "${RUN_LOG}"
fi

date_utc=$(date -u +"%Y-%m-%d %H:%M")
echo "| ${date_utc} | ${sky_commit} | ${cpu_brand} | ${N} | ${wall_s} | ${per_fixture_ms} | ${cache_mb} | ${cache_per_fixture_mb} | ${samples_csv} |" \
    >> "${RUN_LOG}"

echo ""
echo "─── scale-projection result ───"
echo "  N:             ${N} fixtures"
echo "  Wall:          ${wall_s} s  (${per_fixture_ms} ms/fixture)"
echo "  Peak GOCACHE:  ${cache_mb} MB  (${cache_per_fixture_mb} MB/fixture)"
echo "  Samples:       ${samples_csv}"
echo "  Run log:       ${RUN_LOG}"
echo ""
echo "→ slope = ${per_fixture_ms} ms/fixture × ${cache_per_fixture_mb} MB/fixture"
echo "→ extrapolation: 500 fixtures ≈ $(( 500 * per_fixture_ms / 1000 )) s wall, $(awk -v p=${cache_per_fixture_mb} 'BEGIN{printf "%.0f", 500*p/1024}') GB cache"
echo ""
