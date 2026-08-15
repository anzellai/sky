#!/usr/bin/env bash
# scripts/test-mem-guard.sh — gate for scripts/mem-guard.sh's pressure signal.
#
# mem-guard exists to stop a runaway build paging the Mac to a standstill. For
# most of its life it measured only free+inactive memory, which on macOS is the
# wrong instrument: the kernel compresses aggressively, so that number reads
# healthy while the machine pages itself to death. Measured during a real
# incident on 2026-08-15, free+inactive held ~1500MB — above the 1200MB floor,
# so the guard never fired — while swap ran 11.4G of 12.3G and the host had to
# be hard-killed.
#
# This gate pins the decision function against that incident and against the
# false positives a naive swap threshold would produce.

set -euo pipefail

GUARD="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/mem-guard.sh"
[[ -r "$GUARD" ]] || { echo "cannot read $GUARD" >&2; exit 1; }

# Pull the two measurement functions out of the guard rather than restating
# them. A copy here would pass while the guard itself was wrong — the exact
# defect class this repo keeps finding.
eval "$(sed -n '/^swap_pct()/,/^}/p' "$GUARD")"
declare -F swap_pct >/dev/null || { echo "FAIL: swap_pct not found in mem-guard.sh" >&2; exit 1; }

SYS_FLOOR_MB="$(sed -n 's/^SYS_FLOOR_MB="\${MEM_GUARD_SYS_FLOOR_MB:-\([0-9]*\)}"/\1/p' "$GUARD")"
SWAP_PCT="$(sed -n 's/^SWAP_PCT="\${MEM_GUARD_SWAP_PCT:-\([0-9]*\)}"/\1/p' "$GUARD")"
[[ -n "$SYS_FLOOR_MB" && -n "$SWAP_PCT" ]] || {
    echo "FAIL: could not read SYS_FLOOR_MB/SWAP_PCT defaults from mem-guard.sh" >&2
    echo "      (the guard's shape changed; this gate is stale, not the guard)" >&2
    exit 1
}

# The decision under test, mirroring the guard's loop.
verdict() {
    local free="$1" swap="$2" floor="$SYS_FLOOR_MB"
    (( SWAP_PCT > 0 )) && (( swap >= SWAP_PCT )) && floor=$(( SYS_FLOOR_MB * 2 ))
    (( free < floor )) && echo FIRE || echo quiet
}

pass=0 fail=0
expect() {
    local name="$1" free="$2" swap="$3" want="$4" got
    got="$(verdict "$free" "$swap")"
    if [[ "$got" == "$want" ]]; then
        pass=$(( pass + 1 )); printf '  ok    %-38s free=%-7s swap=%-4s %s\n' "$name" "${free}MB" "${swap}%" "$got"
    else
        fail=$(( fail + 1 )); printf '  FAIL  %-38s free=%-7s swap=%-4s got=%s want=%s\n' "$name" "${free}MB" "${swap}%" "$got" "$want"
    fi
}

echo "mem-guard pressure signal (floor=${SYS_FLOOR_MB}MB, swap ceiling=${SWAP_PCT}%)"

# The regression. This is the case the old guard could not see.
expect "the 2026-08-15 incident"            1500 93 FIRE
# ...and the proof it was invisible before: same memory, swap signal ignored.
expect "same memory, swap signal ignored"   1500  0 quiet

# Pre-existing behaviour must not change.
expect "low memory, swap has headroom"       900 40 FIRE
expect "comfortable memory"                 8000 10 quiet

# False positives a bare swap threshold would produce. macOS never reclaims
# swap eagerly, so high utilisation is a high-water mark, not thrash.
expect "swap full, memory plentiful"        8000 95 quiet
expect "swap at ceiling, memory plentiful"  8000 80 quiet

# Boundaries.
expect "free exactly at raised floor"  $(( SYS_FLOOR_MB * 2 ))     95 quiet
expect "free one below raised floor"   $(( SYS_FLOOR_MB * 2 - 1 )) 95 FIRE
expect "free exactly at base floor"    "$SYS_FLOOR_MB"             10 quiet
expect "free one below base floor"     $(( SYS_FLOOR_MB - 1 ))     10 FIRE

# swap_pct must degrade to 0 — "no swap pressure" — rather than to a kill
# storm, when sysctl is absent or its format changes.
echo "swap_pct() parsing"
got="$(swap_pct)"
if [[ "$got" =~ ^[0-9]+$ ]] && (( got >= 0 && got <= 100 )); then
    pass=$(( pass + 1 )); printf '  ok    %-38s %s%%\n' "against this host's sysctl" "$got"
else
    fail=$(( fail + 1 )); printf '  FAIL  %-38s got=%q, want an integer 0-100\n' "against this host's sysctl" "$got"
fi

# Shadow sysctl with a failing stub rather than blanking PATH — blanking it
# removes awk as well, which tests nothing about the guard. The guard runs
# under `set -euo pipefail`, so a failing sysctl must not fail the pipeline:
# that would take the whole watchdog down, turning a missing reading into no
# guard at all.
stub="$(mktemp -d)"
trap 'rm -rf "$stub"' EXIT
printf '#!/bin/sh\nexit 1\n' > "$stub/sysctl"
chmod +x "$stub/sysctl"

got="$(PATH="$stub:$PATH" bash -c "set -euo pipefail; $(declare -f swap_pct); swap_pct" 2>/dev/null || echo GUARD_DIED)"
if [[ "$got" == "0" ]]; then
    pass=$(( pass + 1 )); printf '  ok    %-38s 0\n' "a failing sysctl degrades to 0"
else
    fail=$(( fail + 1 )); printf '  FAIL  %-38s got=%q, want 0\n' "a failing sysctl degrades to 0" "$got"
    [[ "$got" == "GUARD_DIED" ]] && printf '        under set -euo pipefail the watchdog exits — no guard at all\n'
fi

# A guard that dies under load is worse than no guard, because the absence is
# silent. This has happened twice in one day here, both times during a cargo
# peak: fork() failed, `set -euo pipefail` fired, and the watchdog exited
# without a word. Every probe in the loop is a fork, so a failing probe must
# skip the tick and retry — it must never exit.
echo "survives a failing probe"
probe_stub="$(mktemp -d)"
printf '#!/bin/sh\nexit 1\n' > "$probe_stub/vm_stat"
chmod +x "$probe_stub/vm_stat"
guard_log="$(mktemp)"

MEM_GUARD_DRY=1 MEM_GUARD_INTERVAL=1 MEM_GUARD_LOG="$guard_log" \
    PATH="$probe_stub:$PATH" "$GUARD" >/dev/null 2>&1 &
guard_pid=$!
sleep 4

if kill -0 "$guard_pid" 2>/dev/null; then
    pass=$(( pass + 1 )); printf '  ok    %-38s still running after 4s\n' "vm_stat fails on every tick"
else
    fail=$(( fail + 1 )); printf '  FAIL  %-38s the watchdog exited — silently, as in the real incident\n' "vm_stat fails on every tick"
fi
kill -TERM "$guard_pid" 2>/dev/null || true
wait "$guard_pid" 2>/dev/null || true

if grep -q 'DEGRADED' "$guard_log" 2>/dev/null; then
    pass=$(( pass + 1 )); printf '  ok    %-38s logged\n' "the degradation is visible"
else
    fail=$(( fail + 1 )); printf '  FAIL  %-38s nothing logged; a silent degradation is the defect\n' "the degradation is visible"
fi
rm -rf "$probe_stub" "$guard_log"

echo
if (( fail )); then
    echo "GATE FAIL — ${pass} passed, ${fail} failed"
    exit 1
fi
echo "GATE PASS — ${pass} passed, 0 failed"
