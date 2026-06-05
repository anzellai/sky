#!/usr/bin/env bash
# scripts/lib/concurrency.sh — single source of truth for parallel
# test worker counts.
#
# Why one helper: every parallel test step (example-sweep.sh,
# Playwright, hspec, go test) needs to pick a worker count. If each
# picks its own number we drift into either under-parallelism (slow
# sweep) or over-parallelism (mem-guard kills look like flakes).
#
# Formula:
#
#   workers = min(
#       cores - 2,                       # leave headroom for OS + mem-guard
#       (free_GB - 2) * 2 / 3,           # 1.5 GB / worker (Sky compile RSS budget)
#       MAX_TEST_WORKERS_OVERRIDE        # operator escape hatch
#   )
#
# The 1.5 GB-per-worker estimate matches what we observe: a `sky build`
# of skyshop peaks at ~1.2 GB RSS, most examples sit at 200-400 MB,
# go build under cgo for the webview example tops out around 800 MB.
# Reserving 1.5 GB gives mem-guard's 6 GB per-process kill threshold
# four reads of safety margin.
#
# Reserving 2 cores: one for mem-guard.sh's poll loop and the system
# shells, one as a graceful overload buffer so a parallel test storm
# doesn't fight the OS for context switches.
#
# Reserving 2 GB free: mem-guard's system floor kicks in at 1.2 GB
# available; reserving 2 GB on top of the worker budget keeps it
# from tripping on routine memory pressure.
#
# Usage:
#
#   source "$(dirname "$0")/lib/concurrency.sh"
#   MAX_WORKERS=$(compute_max_workers)
#   xargs -P "$MAX_WORKERS" ...
#
# Override per-process via env:
#
#   MAX_TEST_WORKERS=2 ./scripts/example-sweep.sh    # constrain to 2
#   MAX_TEST_WORKERS=8 ./scripts/example-sweep.sh    # crank up

set -u

# count_cores — cross-platform CPU core count.
# macOS: sysctl. Linux: nproc. Other: assume 4.
count_cores() {
    if command -v sysctl >/dev/null 2>&1 && sysctl -n hw.ncpu >/dev/null 2>&1; then
        sysctl -n hw.ncpu
    elif command -v nproc >/dev/null 2>&1; then
        nproc
    else
        echo 4
    fi
}

# free_memory_gb — best-effort available memory in whole GB.
# macOS: vm_stat (free + inactive + speculative pages × 4096).
# Linux: /proc/meminfo MemAvailable.
# Other: assume 8 GB.
#
# We use AVAILABLE rather than just free because inactive pages are
# the kernel's reclaimable cache — they're effectively free for new
# allocations. mem-guard.sh uses the same accounting.
free_memory_gb() {
    if command -v vm_stat >/dev/null 2>&1; then
        # macOS path. vm_stat's first line reports page size, e.g.
        # "Mach Virtual Memory Statistics: (page size of 16384 bytes)"
        # on Apple Silicon (16 KB) vs 4096 on Intel. Reading it from
        # the header keeps us portable across architectures.
        vm_stat | awk '
            NR == 1 {
                match($0, /page size of [0-9]+ bytes/)
                if (RLENGTH > 0) {
                    page_size = substr($0, RSTART + 13, RLENGTH - 19) + 0
                }
                if (page_size == 0) page_size = 4096
            }
            /Pages free/        {f=$3+0}
            /Pages inactive/    {i=$3+0}
            /Pages speculative/ {s=$3+0}
            END {
                if (f + i + s == 0) { print "8"; exit }
                printf "%d", (f + i + s) * page_size / 1024 / 1024 / 1024
            }'
    elif [ -r /proc/meminfo ]; then
        awk '/MemAvailable/ {printf "%d", $2 / 1024 / 1024}' /proc/meminfo
    else
        echo 8
    fi
}

# compute_max_workers — the canonical formula. Stdout is a single
# integer >= 1.
#
# Respects MAX_TEST_WORKERS as a hard override (operator says they
# know what they're doing). If MAX_TEST_WORKERS is set to 0 or
# unparseable, falls through to the computed default.
compute_max_workers() {
    if [ -n "${MAX_TEST_WORKERS:-}" ] && [ "${MAX_TEST_WORKERS}" -gt 0 ] 2>/dev/null; then
        echo "$MAX_TEST_WORKERS"
        return 0
    fi

    local cores free_gb cpu_workers mem_workers workers
    cores=$(count_cores)
    free_gb=$(free_memory_gb)

    # CPU budget: reserve 2 cores.
    cpu_workers=$(( cores - 2 ))
    [ "$cpu_workers" -lt 1 ] && cpu_workers=1

    # Memory budget: reserve 2 GB, assume 1.5 GB per worker.
    mem_workers=$(( (free_gb - 2) * 2 / 3 ))
    [ "$mem_workers" -lt 1 ] && mem_workers=1

    # Workers is the floor of the two budgets.
    if [ "$cpu_workers" -le "$mem_workers" ]; then
        workers=$cpu_workers
    else
        workers=$mem_workers
    fi

    [ "$workers" -lt 1 ] && workers=1
    echo "$workers"
}

# describe_concurrency — diagnostic line for test runners to print.
# Format: "concurrency: N workers (cores=C, free=GG)"
describe_concurrency() {
    local cores free_gb workers override
    cores=$(count_cores)
    free_gb=$(free_memory_gb)
    workers=$(compute_max_workers)
    override=""
    [ -n "${MAX_TEST_WORKERS:-}" ] && [ "${MAX_TEST_WORKERS}" -gt 0 ] 2>/dev/null \
        && override=" [MAX_TEST_WORKERS=${MAX_TEST_WORKERS} override]"
    echo "concurrency: ${workers} workers (cores=${cores}, free=${free_gb}GB)${override}"
}
