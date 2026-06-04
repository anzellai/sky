#!/usr/bin/env bash
#
# disk-pressure-experiment.sh — Experiment #3 from the v0.16.2
# composite-test-apps RFC.
#
# Fills the disk to ~70% (via a placeholder file under /tmp) BEFORE
# the cabal-test sweep. Then runs the sweep. Asserts: it either
# completes within the cold-cache budget OR fails fast with a clear
# error — NEVER hangs or wedges in a half-built state.
#
# What we're guarding against:
#   - go-build cache writes that don't check ENOSPC
#   - cabal sdist that writes a 2 GB blob to /tmp without checking
#   - Sky-emitted main.go writes that fail mid-stream and leave a
#     half-truncated file the next build can't parse
#
# Per CLAUDE.md §6 disk hygiene: this is the test that catches the
# class of bugs the 2026-06-04 SkyDeploy 0.15.59 → 0.16.1 bump
# nearly hit (81 GB GOCACHE growth on a 73 GB-free disk).
#
# THIS SCRIPT IS DESTRUCTIVE in the sense that it consumes ~30 GB
# of disk for the duration of the sweep then frees it. It will
# REFUSE to run if you have less than 50 GB free at the start
# (need room for both the placeholder + the sweep's working set).
#
# Usage:
#   ./docs/v0.16.x-console/composite-test-experiments/disk-pressure-experiment.sh
#
# Optional env:
#   PRESSURE_GB=<int>  — how much placeholder to create (default 30)
#   SKIP_CABAL=1       — dry-run

set -euo pipefail

EXP_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${EXP_DIR}/../../.." && pwd)"
RUN_LOG="${EXP_DIR}/disk-pressure-RUN_LOG.md"
PRESSURE_GB="${PRESSURE_GB:-30}"

cd "${REPO_ROOT}"

# Pre-check: need ≥ 50 GB free OR placeholder + sweep working set.
disk_free_kb=$(df -k . | tail -1 | awk '{print $4}')
disk_free_gb=$(( disk_free_kb / 1024 / 1024 ))
need_gb=$(( PRESSURE_GB + 20 ))  # 20 GB sweep working set
if (( disk_free_gb < need_gb )); then
    echo "[disk-pressure] ✘ ABORT: only ${disk_free_gb} GB free; need ≥ ${need_gb} GB." >&2
    echo "[disk-pressure]   (PRESSURE_GB=${PRESSURE_GB} + 20 GB sweep headroom)" >&2
    exit 2
fi

sky_commit=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
cpu_brand=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo "unknown")

PLACEHOLDER="${TMPDIR:-/tmp}/sky-disk-pressure-placeholder.bin"

cleanup() {
    if [[ -f "${PLACEHOLDER}" ]]; then
        echo "[disk-pressure] removing placeholder (freeing ~${PRESSURE_GB} GB)"
        rm -f "${PLACEHOLDER}"
    fi
}
trap cleanup EXIT INT TERM

echo "[disk-pressure] sky commit ${sky_commit} on ${cpu_brand}, free ${disk_free_gb} GB"
echo "[disk-pressure] creating ${PRESSURE_GB} GB placeholder at ${PLACEHOLDER}"

# mkfile is faster than dd on macOS for sparse-pre-sized files,
# but we want REAL bytes consumed (sparse files wouldn't pressure
# the FS). Use dd with `bs=1m count=N*1024`.
dd if=/dev/zero of="${PLACEHOLDER}" bs=1m count=$(( PRESSURE_GB * 1024 )) status=progress 2>&1 \
    | tail -3

disk_after_kb=$(df -k . | tail -1 | awk '{print $4}')
disk_after_gb=$(( disk_after_kb / 1024 / 1024 ))
echo "[disk-pressure] post-placeholder free: ${disk_after_gb} GB"

# ─── Run the sweep ─────────────────────────────────────────────────

epoch_start=$(date +%s)

if [[ "${SKIP_CABAL:-0}" == "1" ]]; then
    echo "[disk-pressure] SKIP_CABAL=1 — dry-run, sleeping 10 s"
    sleep 10
    cabal_exit=0
else
    set +e
    timeout 3600 ./scripts/cabal-test.sh \
        --test-show-details=direct \
        --test-options='--skip=Sky.Build.VerifyAll' \
        > "${EXP_DIR}/disk-pressure-last-run.log" 2>&1
    cabal_exit=$?
    set -e
fi

epoch_end=$(date +%s)
wall_s=$(( epoch_end - epoch_start ))

disk_end_kb=$(df -k . | tail -1 | awk '{print $4}')
disk_end_gb=$(( disk_end_kb / 1024 / 1024 ))

# ─── Report ────────────────────────────────────────────────────────

finished_line=$(grep -E "^[0-9]+ examples?, " "${EXP_DIR}/disk-pressure-last-run.log" 2>/dev/null | tail -1 || echo "")
example_summary="(none)"
[[ -n "${finished_line}" ]] && example_summary="${finished_line}"

# Classify outcome:
#   ✓ "passed under pressure"        — cabal exit 0
#   ⚠ "failed FAST + clear"          — cabal exit ≠ 0 BUT log mentions ENOSPC / no space
#   ✘ "failed without diagnosis"     — cabal exit ≠ 0 AND no ENOSPC mention
status="?"
case "${cabal_exit}" in
    0)
        status="✓ passed under pressure"
        ;;
    *)
        if grep -qE "no space left|ENOSPC|out of space|cannot allocate" "${EXP_DIR}/disk-pressure-last-run.log" 2>/dev/null; then
            status="⚠ failed-fast + clear ENOSPC error (acceptable)"
        else
            status="✘ failed without disk-related diagnosis (BAD)"
        fi
        ;;
esac

if [[ ! -f "${RUN_LOG}" ]]; then
    {
        echo "# Disk-pressure cabal-test — Experiment #3"
        echo ""
        echo "Each row: cabal-test sweep with disk artificially pressured."
        echo "Acceptable: full pass OR fail-fast with clear ENOSPC."
        echo "Failure: hang OR fail without disk-related diagnosis."
        echo ""
        echo "| Date (UTC) | sky commit | CPU | Pressure (GB) | Free start (GB) | Free after pressure (GB) | Free end (GB) | Wall (s) | cabal exit | Examples | Status |"
        echo "|---|---|---|---|---|---|---|---|---|---|---|"
    } > "${RUN_LOG}"
fi

date_utc=$(date -u +"%Y-%m-%d %H:%M")
echo "| ${date_utc} | ${sky_commit} | ${cpu_brand} | ${PRESSURE_GB} | ${disk_free_gb} | ${disk_after_gb} | ${disk_end_gb} | ${wall_s} | ${cabal_exit} | ${example_summary} | ${status} |" \
    >> "${RUN_LOG}"

echo ""
echo "─── disk-pressure result ───"
echo "  Pressure:      ${PRESSURE_GB} GB placeholder"
echo "  Free start:    ${disk_free_gb} GB"
echo "  Free after:    ${disk_after_gb} GB"
echo "  Free end:      ${disk_end_gb} GB (after cleanup)"
echo "  Wall:          ${wall_s} s"
echo "  cabal exit:    ${cabal_exit}"
echo "  ${example_summary}"
echo "  Status:        ${status}"
echo ""

# Status icon controls exit code: ✓/⚠ both pass, ✘ fails the experiment.
case "${status}" in
    "✘"*) exit 1 ;;
    *) exit 0 ;;
esac
