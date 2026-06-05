#!/usr/bin/env bash
#
# scripts/cabal-test.sh — runs `cabal test` with a SIZE-BOUNDED
# isolated GOCACHE so the per-cabal-run cache bloat (driven by Go
# generic monomorphisation across 100+ test fixtures — see
# https://github.com/golang/go/issues/76337) doesn't pollute the
# user's main ~/Library/Caches/go-build directory AND doesn't fill
# the host disk mid-run.
#
# Why: Sky.Live's runtime uses generics heavily (Cfg_R[T1 any],
# SkyTask[Error, T], etc). Each fixture's Sky→Go output instantiates
# these with different concrete types. Go caches each
# instantiation separately, content-addressed. Across 100+ unique
# fixtures, the cache balloons to 100-200 GB, fills disk, and
# kills the test run.
#
# The vast majority of those cache entries will never be hit again
# (each test's fixture is unique). Isolation + bounded growth lets
# us:
#   1. Reap the speedup of intra-run reuse (stdlib, runtime-go
#      non-generic parts) while the cache lives;
#   2. Cap PEAK exposure to disk via a watcher that flushes the
#      cache when it exceeds MAX_GOCACHE_GB (default 8). Go is
#      content-addressed, so a mid-run flush just makes the next
#      `go build` re-cache from scratch — never breaks correctness;
#   3. Reap final cleanup via the EXIT trap.
#
# v0.16.4-B5 fix: a 3-cycle disk-out blocker on the v0.16.4 hub
# work forced this watcher in. Pre-fix, the cache grew unbounded
# during the run (30+ GB observed) and any disk pressure killed
# cabal mid-test with ENOSPC.
#
# Usage:
#   ./scripts/cabal-test.sh                            # full sweep
#   ./scripts/cabal-test.sh --test-options "--match \"/Sky.Lsp/\""
#   SKY_SKIP_SWEEP=1 ./scripts/cabal-test.sh           # skip ExampleSweep
#   MAX_GOCACHE_GB=12 ./scripts/cabal-test.sh          # higher cap (more disk OK)
#   GOCACHE_FLUSH_INTERVAL=15 ./scripts/cabal-test.sh  # check every 15s (default 30)
#

set -euo pipefail

# Detect a usable TMPDIR.  Cabal honours TMPDIR; we want our cache
# colocated with whatever it uses so cleanup is unambiguous.
WORKROOT="${TMPDIR:-/tmp}"
CACHE_DIR=$(mktemp -d "${WORKROOT}/sky-cabal-gocache.XXXXXX")
MAX_GOCACHE_GB="${MAX_GOCACHE_GB:-8}"
GOCACHE_FLUSH_INTERVAL="${GOCACHE_FLUSH_INTERVAL:-30}"
MAX_GOCACHE_BYTES=$((MAX_GOCACHE_GB * 1024 * 1024 * 1024))
echo "[cabal-test] GOCACHE=${CACHE_DIR}"
echo "[cabal-test] cap: ${MAX_GOCACHE_GB} GB; flush check every ${GOCACHE_FLUSH_INTERVAL} s"

# Background watcher: flushes the cache when it exceeds the cap.
# Go's cache is content-addressed (`go-build` SHA prefix dirs); a
# mid-run wipe just forces the next `go build` to re-cache, never
# breaks a build in progress. Each entry is a small file Go creates
# atomically — wiping while Go writes one is at worst a stale-cache-
# miss next call, which Go retries silently.
#
# The watcher runs `du -sb` to size the dir; on macOS that's slow
# on a 8GB dir (~1s) but the bound is loose so cadence-dropped
# checks just mean we flush at slightly above the cap, not below.
cache_watcher() {
    local cache_dir="$1"
    local max_bytes="$2"
    local interval="$3"
    while sleep "$interval"; do
        if [ ! -d "$cache_dir" ]; then
            return 0
        fi
        local size
        size=$(du -sk "$cache_dir" 2>/dev/null | awk '{print $1 * 1024}')
        if [ -n "$size" ] && [ "$size" -gt "$max_bytes" ]; then
            local size_gb=$((size / 1024 / 1024 / 1024))
            echo "[cabal-test] cache hit ${size_gb} GB > ${MAX_GOCACHE_GB} GB cap — flushing"
            # Wipe contents but keep the dir (GOCACHE env var still valid).
            find "$cache_dir" -mindepth 1 -delete 2>/dev/null || true
        fi
    done
}

cache_watcher "$CACHE_DIR" "$MAX_GOCACHE_BYTES" "$GOCACHE_FLUSH_INTERVAL" &
WATCHER_PID=$!

# Trap MUST clean up even on Ctrl-C / kill / cabal failure.
# DO NOT add `exec` below — `exec cabal …` replaces this bash
# process, discarding the trap. The cache survives, leaking
# tens of GB per run. (Bit us 2026-06-04 — 81 GB orphan after
# v0.16.1 cabal sweep — see task #459.)
cleanup() {
    if [ -n "${WATCHER_PID:-}" ]; then
        kill "$WATCHER_PID" 2>/dev/null || true
    fi
    rm -rf "${CACHE_DIR}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

export GOCACHE="${CACHE_DIR}"

# Forward all args to cabal test verbatim. Bash exits with
# cabal's status code; the EXIT trap fires on the way out.
cabal test "$@"
