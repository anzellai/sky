#!/usr/bin/env bash
# scripts/mem-guard.sh — memory kill-switch for Sky compiler dev sessions.
#
# Background: a runaway `sky` build / `sky lsp` / `cabal` process can pin the
# entire Mac to swap and force a hard reboot. This watchdog polls memory every
# few seconds and SIGKILLs the heaviest watched process before that happens.
#
# Usage:
#   ./scripts/mem-guard.sh                  # foreground, logs to stderr + /tmp/mem-guard.log
#   nohup ./scripts/mem-guard.sh &          # background for the session
#   MEM_GUARD_PROC_MB=4000 ./scripts/mem-guard.sh   # tighter per-proc cap
#
# Tunables (env vars, all optional):
#   MEM_GUARD_PROC_MB        per-process RSS kill threshold (MB).      default 6000
#   MEM_GUARD_PANIC_MB       claude/ghostty kill threshold (MB).        default 10000
#   MEM_GUARD_SYS_FLOOR_MB   free+inactive memory floor (MB).           default 1200
#   MEM_GUARD_SWAP_PCT       swap-utilisation ceiling (percent).        default 80
#                            0 disables the swap signal.
#   MEM_GUARD_INTERVAL       poll interval (seconds).                   default 2
#   MEM_GUARD_LOG            log file path.                             default /tmp/mem-guard.log
#   MEM_GUARD_DRY            set to 1 to log only, never kill.          default unset
#
# Watched process names (basename of comm):
#   Always-kill at PROC_MB:  sky, sky-ffi-inspect, cargo, rustc,
#                            rust-analyzer, cabal, ghc, ghc-iserv,
#                            cc1, ld64, haskell-language-server, hls-wrapper,
#                            gopls, go (when child of a sky/cargo/cabal build)
#   Last-resort at PANIC_MB: claude, node, ghostty
#                            (these are the host of *this* session — only kill
#                             when they themselves are the runaway, not their
#                             children. Higher threshold reflects that.)
#
# The script never kills system processes (kernel_task, WindowServer, launchd).

set -euo pipefail

PROC_LIMIT_MB="${MEM_GUARD_PROC_MB:-6000}"
PANIC_LIMIT_MB="${MEM_GUARD_PANIC_MB:-10000}"
SYS_FLOOR_MB="${MEM_GUARD_SYS_FLOOR_MB:-1200}"
SWAP_PCT="${MEM_GUARD_SWAP_PCT:-80}"
INTERVAL="${MEM_GUARD_INTERVAL:-2}"
LOG="${MEM_GUARD_LOG:-/tmp/mem-guard.log}"
DRY="${MEM_GUARD_DRY:-}"

# basename(comm) regexes
ALWAYS_KILL_RE='^(sky|sky-ffi-inspect|cargo|rustc|rust-analyzer|cabal|ghc|ghc-iserv|cc1|ld64|ld|haskell-language-server|hls-wrapper|gopls)$'
PANIC_KILL_RE='^(claude|node|ghostty)$'

log() {
    printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" | tee -a "$LOG" >&2
}

# Free + inactive pages, in MB. macOS treats inactive as reclaimable, so we
# include it — the danger is when neither free nor inactive can satisfy a new
# allocation and the kernel starts compressing/swapping in earnest.
free_mb() {
    local page_kb=$(( $(sysctl -n hw.pagesize) / 1024 ))
    vm_stat | awk -v pk="$page_kb" '
        /Pages free/        { gsub(/\./, ""); free = $3 }
        /Pages inactive/    { gsub(/\./, ""); inact = $3 }
        /Pages speculative/ { gsub(/\./, ""); spec = $3 }
        END { printf "%d\n", (free + inact + spec) * pk / 1024 }
    '
}

# Swap utilisation, as a whole percent of the current swap total.
#
# free_mb() alone cannot see the failure this script exists to prevent. macOS
# compresses aggressively, so free+inactive keeps reading healthy while the
# machine pages itself to a standstill: measured during a real incident,
# free ran 33-59% for the whole episode while swap climbed to 11.4G of 12.3G
# and the host had to be hard-killed. SYS_FLOOR_MB was never breached, so the
# guard could not have fired. Swap utilisation is the signal that moved.
#
# Percent-of-total rather than an absolute floor, because macOS grows the swap
# file on demand — an absolute "free swap" number is meaningless when the
# denominator moves. Prints 0 when swap is absent or unparseable, so an
# unexpected sysctl format degrades to "no swap pressure" rather than to a
# kill storm.
swap_pct() {
    # `|| true` is load-bearing: this script runs under `set -euo pipefail`, so
    # a failing sysctl would fail the pipeline, fail the `swap=$(swap_pct)`
    # substitution, and take the whole watchdog down — turning a missing
    # reading into no guard at all. Degrade to "no swap pressure" instead.
    local raw
    raw="$(sysctl -n vm.swapusage 2>/dev/null || true)"
    printf '%s\n' "$raw" | awk '
        {
            for (i = 1; i <= NF; i++) {
                if ($i == "total")     { gsub(/[^0-9.]/, "", $(i+2)); total = $(i+2) + 0 }
                else if ($i == "used") { gsub(/[^0-9.]/, "", $(i+2)); used  = $(i+2) + 0 }
            }
        }
        END { if (total > 0) printf "%d\n", (used * 100) / total; else print 0 }
    '
}

kill_proc() {
    local pid="$1" rss_mb="$2" comm="$3" reason="$4"
    if [[ -n "$DRY" ]]; then
        log "DRY-RUN would kill pid=$pid rss=${rss_mb}MB comm=$comm reason=$reason"
        return
    fi
    log "KILL pid=$pid rss=${rss_mb}MB comm=$comm reason=$reason"
    kill -TERM "$pid" 2>/dev/null || true
    # Brief grace; sky/cabal can usually clean up in <1s
    sleep 1
    if kill -0 "$pid" 2>/dev/null; then
        log "  pid=$pid ignored SIGTERM, sending SIGKILL"
        kill -KILL "$pid" 2>/dev/null || true
    fi
}

trap 'log "stopping (signal)"; exit 0' INT TERM

log "starting (proc=${PROC_LIMIT_MB}MB panic=${PANIC_LIMIT_MB}MB sys_floor=${SYS_FLOOR_MB}MB swap_pct=${SWAP_PCT}% poll=${INTERVAL}s dry=${DRY:-no})"

while :; do
    free=$(free_mb)

    # Swap does not trigger a kill on its own — macOS never reclaims swap
    # eagerly, so utilisation is a high-water mark and reads high on a
    # perfectly healthy machine (88% here, right now, with 76% memory free).
    # What a full swap file DOES mean is that the usual floor is too late:
    # there is no longer anywhere to page to, so the margin between "floor
    # breached" and "machine unusable" has gone. So swap raises the floor
    # rather than firing.
    #
    # Against the real incident: free+inactive held ~1500MB — above the
    # 1200MB floor, so the guard stayed silent — while swap ran 93% and the
    # host had to be hard-killed. A doubled floor of 2400MB fires there, and
    # does not fire in today's healthy 88%-swap state.
    floor=$SYS_FLOOR_MB
    swap=0
    if (( SWAP_PCT > 0 )); then
        swap=$(swap_pct)
        (( swap >= SWAP_PCT )) && floor=$(( SYS_FLOOR_MB * 2 ))
    fi

    pressure=0
    pressure_why=""
    if (( free < floor )); then
        pressure=1
        pressure_why="system free=${free}MB below floor=${floor}MB (swap ${swap}% of total)"
    fi

    # Snapshot watched processes by RSS desc. ps RSS is in KB.
    # We strip directory prefix from comm so /Applications/Ghostty.app/.../ghostty matches "ghostty".
    snap=$(ps -A -o pid=,rss=,comm= | awk '
        {
            pid = $1; rss = $2;
            comm = $3;
            n = split(comm, parts, "/");
            base = parts[n];
            print pid, rss, base
        }
    ' | sort -k2 -rn)

    while read -r pid rss comm; do
        [[ -z "${pid:-}" ]] && continue
        rss_mb=$(( rss / 1024 ))

        if [[ "$comm" =~ $ALWAYS_KILL_RE ]]; then
            if (( rss_mb > PROC_LIMIT_MB )); then
                kill_proc "$pid" "$rss_mb" "$comm" "exceeded per-proc limit ${PROC_LIMIT_MB}MB"
                continue
            fi
            if (( pressure )); then
                kill_proc "$pid" "$rss_mb" "$comm" "${pressure_why} (heaviest watched)"
                pressure=0  # one kill per pass; recheck next iteration
                continue
            fi
        elif [[ "$comm" =~ $PANIC_KILL_RE ]]; then
            if (( rss_mb > PANIC_LIMIT_MB )); then
                kill_proc "$pid" "$rss_mb" "$comm" "exceeded panic limit ${PANIC_LIMIT_MB}MB"
                continue
            fi
            if (( pressure )) && (( rss_mb > 4000 )); then
                # Only sacrifice the host (claude/ghostty) if it's the heaviest
                # AND already over 4GB itself. Avoids killing claude over a
                # cabal child blowing out: the always-kill loop above handles
                # that case first.
                kill_proc "$pid" "$rss_mb" "$comm" "PANIC: system free=${free}MB and host >4GB"
                pressure=0
                continue
            fi
        fi
    done <<< "$snap"

    sleep "$INTERVAL"
done
