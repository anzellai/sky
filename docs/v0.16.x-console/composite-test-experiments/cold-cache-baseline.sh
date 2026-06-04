#!/usr/bin/env bash
#
# cold-cache-baseline.sh — Experiment #1 from the v0.16.2 composite-
# test-apps RFC.
#
# Measures the cabal-test sweep against a freshly-wiped go-build
# cache + cabal store. Records wall time, peak GOCACHE size during
# the run, peak RSS of any single child process, disk free before
# and after, and the cabal exit code.
#
# Budget invariants (RFC):
#   - Wall time   < 10 min on a 2024-era laptop
#   - Peak GOCACHE< 5 GB
#   - Peak RSS    < 4 GB
#   - Disk floor  > 20 GB free
#
# Result appended to RUN_LOG.md so two runs are comparable.
#
# Usage:
#   ./docs/v0.16.x-console/composite-test-experiments/cold-cache-baseline.sh
#
# Optional env:
#   SKIP_WIPE=1   — keep existing go-build cache (debug only;
#                   invalidates the "cold" claim)
#   SKIP_CABAL=1  — skip the cabal run (dry-run; useful when
#                   developing the script)

set -euo pipefail

# ─── Setup ────────────────────────────────────────────────────────

EXP_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${EXP_DIR}/../../.." && pwd)"
RUN_LOG="${EXP_DIR}/cold-cache-RUN_LOG.md"

cd "${REPO_ROOT}"

# Disk floor: abort if < 20 GB before we start so we don't wedge
# half-way through.
disk_free_kb=$(df -k . | tail -1 | awk '{print $4}')
disk_floor_kb=$((20 * 1024 * 1024))
if [[ "${disk_free_kb}" -lt "${disk_floor_kb}" ]]; then
    echo "[cold-cache] ✘ ABORT: only $((disk_free_kb / 1024 / 1024)) GB free; need ≥ 20 GB." >&2
    exit 2
fi

start_disk_free_kb="${disk_free_kb}"
sky_commit=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
cpu_brand=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo "unknown")
mem_total_bytes=$(sysctl -n hw.memsize 2>/dev/null || echo 0)
mem_total_gb=$(( mem_total_bytes / 1024 / 1024 / 1024 ))

echo "[cold-cache] sky commit ${sky_commit} on ${cpu_brand} / ${mem_total_gb} GB"

# ─── Wipe ──────────────────────────────────────────────────────────
# (a) Main go-build cache. (b) Any orphan GOCACHE from a prior
# scripts/cabal-test.sh run. (c) dist-newstyle (cabal store will
# rebuild, but the spec compile is the same so we keep it).

if [[ "${SKIP_WIPE:-0}" != "1" ]]; then
    echo "[cold-cache] wiping go-build cache + any orphan isolated GOCACHEs"
    go clean -cache 2>/dev/null || true
    rm -rf "${TMPDIR:-/tmp}"/sky-cabal-gocache.* 2>/dev/null || true
else
    echo "[cold-cache] SKIP_WIPE=1 — keeping warm caches (results NOT cold-baseline)"
fi

# ─── Background monitor ────────────────────────────────────────────
# Polls every 10 s, writes max-so-far to MONITOR_OUT. Stops when the
# script's pid (main) ends.

MONITOR_OUT=$(mktemp /tmp/cold-cache-monitor.XXX)
echo "0 0" > "${MONITOR_OUT}"  # peak_gocache_kb peak_rss_kb

(
    parent_pid=$$
    while kill -0 "${parent_pid}" 2>/dev/null; do
        # Peak isolated GOCACHE size (cabal-test.sh creates a per-run dir).
        cache_kb=0
        for d in "${TMPDIR:-/tmp}"/sky-cabal-gocache.*; do
            [[ -d "${d}" ]] || continue
            sz=$(du -sk "${d}" 2>/dev/null | awk '{print $1}')
            [[ -z "${sz}" ]] && sz=0
            (( sz > cache_kb )) && cache_kb=${sz}
        done

        # Peak RSS of any single Sky/cabal child process.
        rss_kb=$(ps -axo rss,comm | awk '
            $2 ~ /(sky|cabal|sky-tests|go|cc|cc1|link|gopls)$/ { if ($1+0 > max) max = $1+0 }
            END { print max+0 }
        ')

        # Read prior maxima and bump.
        prev=$(cat "${MONITOR_OUT}")
        prev_cache=$(echo "${prev}" | awk '{print $1}')
        prev_rss=$(echo "${prev}" | awk '{print $2}')
        new_cache=${prev_cache}
        new_rss=${prev_rss}
        (( cache_kb > prev_cache )) && new_cache=${cache_kb}
        (( rss_kb > prev_rss )) && new_rss=${rss_kb}
        echo "${new_cache} ${new_rss}" > "${MONITOR_OUT}"

        sleep 10
    done
) &
monitor_pid=$!

cleanup() {
    # Stop monitor + tidy.
    kill "${monitor_pid}" 2>/dev/null || true
    rm -f "${MONITOR_OUT}"
}
trap cleanup EXIT INT TERM

# ─── Run the sweep ─────────────────────────────────────────────────

epoch_start=$(date +%s)

if [[ "${SKIP_CABAL:-0}" == "1" ]]; then
    echo "[cold-cache] SKIP_CABAL=1 — dry-run, sleeping 20 s as a stand-in"
    sleep 20
    cabal_exit=0
else
    # Same exact command the v0.16.1 CI uses (per CLAUDE.md):
    #   timeout 3600 ./scripts/cabal-test.sh --test-show-details=direct --test-options='--skip=Sky.Build.VerifyAll'
    set +e
    timeout 3600 ./scripts/cabal-test.sh \
        --test-show-details=direct \
        --test-options='--skip=Sky.Build.VerifyAll' \
        > "${EXP_DIR}/cold-cache-last-run.log" 2>&1
    cabal_exit=$?
    set -e
fi

epoch_end=$(date +%s)
wall_s=$(( epoch_end - epoch_start ))

# Read final peaks. Monitor may still be writing — sleep once + kill.
sleep 1
kill "${monitor_pid}" 2>/dev/null || true
wait "${monitor_pid}" 2>/dev/null || true

peak_cache_kb=$(awk '{print $1}' "${MONITOR_OUT}")
peak_rss_kb=$(awk '{print $2}' "${MONITOR_OUT}")
peak_cache_gb=$(awk -v kb="${peak_cache_kb}" 'BEGIN{printf "%.2f", kb/1024/1024}')
peak_rss_gb=$(awk -v kb="${peak_rss_kb}" 'BEGIN{printf "%.2f", kb/1024/1024}')

end_disk_free_kb=$(df -k . | tail -1 | awk '{print $4}')
disk_delta_gb=$(awk -v s="${start_disk_free_kb}" -v e="${end_disk_free_kb}" 'BEGIN{printf "%.2f", (s-e)/1024/1024}')

# ─── Report ────────────────────────────────────────────────────────

# Hspec output → extract failure count.
finished_line=$(grep -E "^[0-9]+ examples?, " "${EXP_DIR}/cold-cache-last-run.log" 2>/dev/null | tail -1 || echo "")
example_summary="(none)"
[[ -n "${finished_line}" ]] && example_summary="${finished_line}"

# Budget check (RFC):
status="✓"
[[ "${cabal_exit}" -ne 0 ]] && status="✘ exit=${cabal_exit}"
(( wall_s > 600 )) && status="${status} wall>10min"
awk -v g="${peak_cache_gb}" 'BEGIN{exit (g>5)}' && true || status="${status} cache>5GB"
awk -v g="${peak_rss_gb}" 'BEGIN{exit (g>4)}' && true || status="${status} rss>4GB"

# Header on first append.
if [[ ! -f "${RUN_LOG}" ]]; then
    {
        echo "# Cold-cache cabal-test baseline — Experiment #1"
        echo ""
        echo "Each row: a single run on a freshly-wiped go-build cache."
        echo "Budget: wall < 600 s, peak cache < 5 GB, peak RSS < 4 GB."
        echo ""
        echo "| Date (UTC) | sky commit | CPU | RAM | Wall (s) | Peak cache (GB) | Peak RSS (GB) | Disk Δ (GB) | Examples | Status |"
        echo "|---|---|---|---|---|---|---|---|---|---|"
    } > "${RUN_LOG}"
fi

date_utc=$(date -u +"%Y-%m-%d %H:%M")
echo "| ${date_utc} | ${sky_commit} | ${cpu_brand} | ${mem_total_gb} GB | ${wall_s} | ${peak_cache_gb} | ${peak_rss_gb} | ${disk_delta_gb} | ${example_summary} | ${status} |" \
    >> "${RUN_LOG}"

# Console summary.
echo ""
echo "─── cold-cache-baseline result ───"
echo "  sky commit:    ${sky_commit}"
echo "  Wall time:     ${wall_s} s   (budget: < 600 s)"
echo "  Peak GOCACHE:  ${peak_cache_gb} GB   (budget: < 5 GB)"
echo "  Peak RSS:      ${peak_rss_gb} GB   (budget: < 4 GB)"
echo "  Disk used:     ${disk_delta_gb} GB"
echo "  cabal exit:    ${cabal_exit}"
echo "  ${example_summary}"
echo "  Status:        ${status}"
echo "  Run log:       ${RUN_LOG}"
echo "  Run log entry: appended"
echo ""

exit "${cabal_exit}"
