#!/usr/bin/env bash
#
# scripts/cabal-test.sh — runs `cabal test` with an isolated GOCACHE
# so the per-cabal-run cache bloat (driven by Go generic
# monomorphisation across 100+ test fixtures — see
# https://github.com/golang/go/issues/76337) doesn't pollute the
# user's main ~/Library/Caches/go-build directory.
#
# Why: Sky.Live's runtime uses generics heavily (Cfg_R[T1 any],
# SkyTask[Error, T], etc). Each fixture's Sky→Go output instantiates
# these with different concrete types. Go caches each
# instantiation separately, content-addressed. Across 100+ unique
# fixtures, the cache balloons to 100-200 GB, fills disk, and
# kills the test run.
#
# The vast majority of those cache entries will never be hit again
# (each test's fixture is unique). Isolating them in a per-run temp
# dir lets us:
#   1. Reap the speedup of intra-run reuse (stdlib, runtime-go
#      non-generic parts) while the cache lives;
#   2. Cap total exposure to disk by sweeping the temp dir on exit.
#
# Usage:
#   ./scripts/cabal-test.sh                      # full sweep
#   ./scripts/cabal-test.sh --test-options "--match \"/Sky.Lsp/\""
#   SKY_SKIP_SWEEP=1 ./scripts/cabal-test.sh     # skip ExampleSweep
#

set -euo pipefail

# Detect a usable TMPDIR.  Cabal honours TMPDIR; we want our cache
# colocated with whatever it uses so cleanup is unambiguous.
WORKROOT="${TMPDIR:-/tmp}"
CACHE_DIR=$(mktemp -d "${WORKROOT}/sky-cabal-gocache.XXXXXX")
echo "[cabal-test] GOCACHE=${CACHE_DIR}"

# Trap MUST clean up even on Ctrl-C / kill / cabal failure.
trap 'rm -rf "${CACHE_DIR}" 2>/dev/null || true' EXIT INT TERM

export GOCACHE="${CACHE_DIR}"

# Forward all args to cabal test verbatim.
exec cabal test "$@"
