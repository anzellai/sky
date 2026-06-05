#!/usr/bin/env bash
#
# warm-cache-baseline.sh — Experiment #2 from the v0.16.2 composite-
# test-apps RFC.
#
# Run IMMEDIATELY after a cold-cache run completes. Measures wall
# time on the SECOND run, which establishes the cache-hit ratio. A
# healthy design has warm runs ≤ 2 min (most cabal-store +
# go-build cache entries still valid).
#
# This script does NOT wipe the go-build cache — that's the whole
# point. It also bypasses scripts/cabal-test.sh's per-run isolated
# GOCACHE (which would be 0% hit) and reuses the system cache.
#
# Budget invariant (RFC):
#   - Wall time < 2 min on a 2024-era laptop
#
# Usage:
#   ./docs/v0.16.x-console/composite-test-experiments/warm-cache-baseline.sh
#
# Optional env:
#   SKIP_CABAL=1  — dry-run (useful when developing the script)

set -euo pipefail

EXP_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${EXP_DIR}/../../.." && pwd)"
RUN_LOG="${EXP_DIR}/warm-cache-RUN_LOG.md"

cd "${REPO_ROOT}"

sky_commit=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
cpu_brand=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo "unknown")
mem_total_bytes=$(sysctl -n hw.memsize 2>/dev/null || echo 0)
mem_total_gb=$(( mem_total_bytes / 1024 / 1024 / 1024 ))

# Sanity check: there SHOULD be a go-build cache present. If it's
# empty we're effectively cold and the result will lie.
sys_cache_kb=$(du -sk "${HOME}/Library/Caches/go-build" 2>/dev/null | awk '{print $1}')
sys_cache_kb=${sys_cache_kb:-0}
sys_cache_mb=$(( sys_cache_kb / 1024 ))
if (( sys_cache_kb < 1024 * 100 )); then  # < 100 MB
    echo "[warm-cache] ⚠ go-build cache is small (${sys_cache_mb} MB)."
    echo "[warm-cache]   Result may not reflect a true warm-cache baseline."
    echo "[warm-cache]   Recommend: run cold-cache-baseline.sh first, then this script."
fi

echo "[warm-cache] sky commit ${sky_commit} on ${cpu_brand} / ${mem_total_gb} GB"
echo "[warm-cache] go-build cache before: ${sys_cache_mb} MB"

# ─── Run (no wipe — keep warm) ─────────────────────────────────────

epoch_start=$(date +%s)

if [[ "${SKIP_CABAL:-0}" == "1" ]]; then
    echo "[warm-cache] SKIP_CABAL=1 — dry-run, sleeping 5 s as a stand-in"
    sleep 5
    cabal_exit=0
else
    # Critically: do NOT use scripts/cabal-test.sh here. That isolates
    # GOCACHE per-run, defeating the cache. Use bare `cabal test`
    # against the system GOCACHE so cache hits surface.
    set +e
    timeout 600 cabal test \
        --test-show-details=direct \
        --test-options='--skip=Sky.Build.VerifyAll' \
        > "${EXP_DIR}/warm-cache-last-run.log" 2>&1
    cabal_exit=$?
    set -e
fi

epoch_end=$(date +%s)
wall_s=$(( epoch_end - epoch_start ))

sys_cache_kb_after=$(du -sk "${HOME}/Library/Caches/go-build" 2>/dev/null | awk '{print $1}')
sys_cache_kb_after=${sys_cache_kb_after:-0}
sys_cache_mb_after=$(( sys_cache_kb_after / 1024 ))
cache_delta_mb=$(( sys_cache_mb_after - sys_cache_mb ))

# ─── Report ────────────────────────────────────────────────────────

finished_line=$(grep -E "^[0-9]+ examples?, " "${EXP_DIR}/warm-cache-last-run.log" 2>/dev/null | tail -1 || echo "")
example_summary="(none)"
[[ -n "${finished_line}" ]] && example_summary="${finished_line}"

status="✓"
[[ "${cabal_exit}" -ne 0 ]] && status="✘ exit=${cabal_exit}"
(( wall_s > 120 )) && status="${status} wall>2min"

if [[ ! -f "${RUN_LOG}" ]]; then
    {
        echo "# Warm-cache cabal-test baseline — Experiment #2"
        echo ""
        echo "Each row: a single run on a previously-warmed go-build cache."
        echo "Budget: wall < 120 s (cache hit ratio dominates)."
        echo ""
        echo "| Date (UTC) | sky commit | CPU | RAM | Wall (s) | Cache before (MB) | Cache after (MB) | Δ (MB) | Examples | Status |"
        echo "|---|---|---|---|---|---|---|---|---|---|"
    } > "${RUN_LOG}"
fi

date_utc=$(date -u +"%Y-%m-%d %H:%M")
echo "| ${date_utc} | ${sky_commit} | ${cpu_brand} | ${mem_total_gb} GB | ${wall_s} | ${sys_cache_mb} | ${sys_cache_mb_after} | ${cache_delta_mb} | ${example_summary} | ${status} |" \
    >> "${RUN_LOG}"

echo ""
echo "─── warm-cache-baseline result ───"
echo "  sky commit:    ${sky_commit}"
echo "  Wall time:     ${wall_s} s   (budget: < 120 s)"
echo "  Cache before:  ${sys_cache_mb} MB"
echo "  Cache after:   ${sys_cache_mb_after} MB   (Δ ${cache_delta_mb} MB)"
echo "  cabal exit:    ${cabal_exit}"
echo "  ${example_summary}"
echo "  Status:        ${status}"
echo "  Run log:       ${RUN_LOG}"
echo ""

exit "${cabal_exit}"
