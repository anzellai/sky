#!/usr/bin/env bash
#
# memory-pressure-experiment.sh — Experiment #4 from the v0.16.2
# composite-test-apps RFC.
#
# Runs the cabal-test sweep with a controlled memory hog process
# running alongside. Asserts: mem-guard.sh fires before the system
# OOMs (per CLAUDE.md §1).
#
# What we're guarding against:
#   - The 2026-05 force-poweroff incident (runaway sky/ghc consumed
#     all RAM; macOS killed power; corrupted disk)
#   - mem-guard regressing silently (a kill threshold raised too
#     high, or the script dying without notice)
#
# Mechanics: spawns a Python process that allocates HOG_GB GB of
# bytestring (deliberately not freed). cabal-test runs as normal.
# We tail mem-guard's log to detect when (if) it kills anything.
#
# Acceptable outcomes:
#   ✓ mem-guard kills the hog before OOM; cabal-test continues
#     and finishes
#   ⚠ cabal-test fails fast (timeout / OOM) but the laptop survived
#   ✘ no mem-guard event AND no failure: hog ate cabal's working
#     set and we're flying without seatbelts
#
# Usage:
#   ./docs/v0.16.x-console/composite-test-experiments/memory-pressure-experiment.sh
#
# Optional env:
#   HOG_GB=<int>  — placeholder memory to allocate (default 4)
#   SKIP_CABAL=1  — dry-run

set -euo pipefail

HOG_GB="${HOG_GB:-4}"
EXP_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${EXP_DIR}/../../.." && pwd)"
RUN_LOG="${EXP_DIR}/memory-pressure-RUN_LOG.md"
MEM_GUARD_LOG="/tmp/mem-guard.out"

cd "${REPO_ROOT}"

# Pre-check: mem-guard.sh must already be running.
if ! pgrep -f mem-guard.sh > /dev/null; then
    echo "[memory-pressure] ✘ ABORT: scripts/mem-guard.sh isn't running." >&2
    echo "[memory-pressure]   Start it per CLAUDE.md §1:" >&2
    echo "[memory-pressure]     nohup ./scripts/mem-guard.sh > /tmp/mem-guard.out 2>&1 &" >&2
    exit 2
fi

# Need ≥ 8 GB RAM; otherwise HOG_GB=4 would itself OOM small machines.
mem_total_bytes=$(sysctl -n hw.memsize 2>/dev/null || echo 0)
mem_total_gb=$(( mem_total_bytes / 1024 / 1024 / 1024 ))
if (( mem_total_gb < 8 )); then
    echo "[memory-pressure] ✘ ABORT: only ${mem_total_gb} GB RAM; need ≥ 8 GB." >&2
    exit 2
fi

sky_commit=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
cpu_brand=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo "unknown")

# Snapshot mem-guard log size so we can grep ONLY the deltas at end.
guard_log_start_size=0
[[ -f "${MEM_GUARD_LOG}" ]] && guard_log_start_size=$(wc -c < "${MEM_GUARD_LOG}")

echo "[memory-pressure] sky commit ${sky_commit} on ${cpu_brand} / ${mem_total_gb} GB"
echo "[memory-pressure] spawning ${HOG_GB} GB Python hog"

# ─── Spawn the hog ─────────────────────────────────────────────────

python3 -c "
import time, sys
gb = ${HOG_GB}
chunk_size = 1024 * 1024 * 1024  # 1 GB
chunks = []
for i in range(gb):
    chunks.append(b'x' * chunk_size)
    print(f'allocated {i+1} GB', flush=True)
print(f'holding {gb} GB; sleeping', flush=True)
time.sleep(3600)
" > /tmp/memhog.log 2>&1 &
hog_pid=$!

cleanup() {
    if kill -0 "${hog_pid}" 2>/dev/null; then
        echo "[memory-pressure] killing hog pid ${hog_pid}"
        kill -9 "${hog_pid}" 2>/dev/null || true
    fi
    rm -f /tmp/memhog.log
}
trap cleanup EXIT INT TERM

# Wait for hog to allocate.
sleep 5
echo "[memory-pressure] hog allocation log:"
tail -3 /tmp/memhog.log

# ─── Run the sweep ─────────────────────────────────────────────────

epoch_start=$(date +%s)

if [[ "${SKIP_CABAL:-0}" == "1" ]]; then
    echo "[memory-pressure] SKIP_CABAL=1 — dry-run, sleeping 15 s"
    sleep 15
    cabal_exit=0
else
    set +e
    timeout 3600 ./scripts/cabal-test.sh \
        --test-show-details=direct \
        --test-options='--skip=Sky.Build.VerifyAll' \
        > "${EXP_DIR}/memory-pressure-last-run.log" 2>&1
    cabal_exit=$?
    set -e
fi

epoch_end=$(date +%s)
wall_s=$(( epoch_end - epoch_start ))

# Check mem-guard log for new entries.
mem_guard_killed=0
mem_guard_messages=""
if [[ -f "${MEM_GUARD_LOG}" ]]; then
    guard_log_end_size=$(wc -c < "${MEM_GUARD_LOG}")
    if (( guard_log_end_size > guard_log_start_size )); then
        delta=$(tail -c $(( guard_log_end_size - guard_log_start_size )) "${MEM_GUARD_LOG}" 2>/dev/null)
        if echo "${delta}" | grep -qE "KILL|killed|over budget"; then
            mem_guard_killed=1
            mem_guard_messages=$(echo "${delta}" | grep -E "KILL|killed|over budget" | head -3 | tr '\n' ';')
        fi
    fi
fi

# ─── Report ────────────────────────────────────────────────────────

finished_line=$(grep -E "^[0-9]+ examples?, " "${EXP_DIR}/memory-pressure-last-run.log" 2>/dev/null | tail -1 || echo "")
example_summary="(none)"
[[ -n "${finished_line}" ]] && example_summary="${finished_line}"

if (( mem_guard_killed == 1 )); then
    if (( cabal_exit == 0 )); then
        status="✓ mem-guard fired + cabal survived"
    else
        status="⚠ mem-guard fired + cabal failed-fast (acceptable)"
    fi
else
    if (( cabal_exit == 0 )); then
        status="✘ cabal passed WITHOUT mem-guard firing (no seatbelt)"
    else
        status="✘ cabal failed AND mem-guard didn't fire (bad)"
    fi
fi

if [[ ! -f "${RUN_LOG}" ]]; then
    {
        echo "# Memory-pressure cabal-test — Experiment #4"
        echo ""
        echo "Each row: cabal-test sweep with a Python memory hog running."
        echo "Acceptable: mem-guard fires before OOM, cabal survives OR fails fast."
        echo "Failure: cabal completes without mem-guard firing (no seatbelt)."
        echo ""
        echo "| Date (UTC) | sky commit | CPU | RAM | Hog (GB) | Wall (s) | cabal exit | mem-guard | Examples | Status |"
        echo "|---|---|---|---|---|---|---|---|---|---|"
    } > "${RUN_LOG}"
fi

date_utc=$(date -u +"%Y-%m-%d %H:%M")
guard_col="no"
(( mem_guard_killed == 1 )) && guard_col="YES (${mem_guard_messages})"
echo "| ${date_utc} | ${sky_commit} | ${cpu_brand} | ${mem_total_gb} GB | ${HOG_GB} | ${wall_s} | ${cabal_exit} | ${guard_col} | ${example_summary} | ${status} |" \
    >> "${RUN_LOG}"

echo ""
echo "─── memory-pressure result ───"
echo "  RAM total:     ${mem_total_gb} GB"
echo "  Hog held:      ${HOG_GB} GB"
echo "  Wall:          ${wall_s} s"
echo "  cabal exit:    ${cabal_exit}"
echo "  mem-guard:     $( (( mem_guard_killed == 1 )) && echo 'fired' || echo 'silent' )"
[[ -n "${mem_guard_messages}" ]] && echo "  events:        ${mem_guard_messages}"
echo "  ${example_summary}"
echo "  Status:        ${status}"
echo ""

case "${status}" in
    "✘"*) exit 1 ;;
    *) exit 0 ;;
esac
