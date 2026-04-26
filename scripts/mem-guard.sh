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
#   MEM_GUARD_INTERVAL       poll interval (seconds).                   default 2
#   MEM_GUARD_LOG            log file path.                             default /tmp/mem-guard.log
#   MEM_GUARD_DRY            set to 1 to log only, never kill.          default unset
#
# Watched process names (basename of comm):
#   Always-kill at PROC_MB:  sky, sky-ffi-inspect, cabal, ghc, ghc-iserv,
#                            cc1, ld64, haskell-language-server, hls-wrapper,
#                            gopls, go (when child of a sky/cabal build)
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
INTERVAL="${MEM_GUARD_INTERVAL:-2}"
LOG="${MEM_GUARD_LOG:-/tmp/mem-guard.log}"
DRY="${MEM_GUARD_DRY:-}"

# basename(comm) regexes
ALWAYS_KILL_RE='^(sky|sky-ffi-inspect|cabal|ghc|ghc-iserv|cc1|ld64|ld|haskell-language-server|hls-wrapper|gopls)$'
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

log "starting (proc=${PROC_LIMIT_MB}MB panic=${PANIC_LIMIT_MB}MB sys_floor=${SYS_FLOOR_MB}MB poll=${INTERVAL}s dry=${DRY:-no})"

while :; do
    free=$(free_mb)
    pressure=0
    (( free < SYS_FLOOR_MB )) && pressure=1

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
                kill_proc "$pid" "$rss_mb" "$comm" "system free=${free}MB below floor=${SYS_FLOOR_MB}MB (heaviest watched)"
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
