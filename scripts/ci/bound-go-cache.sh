#!/usr/bin/env bash
#
# Bound the Go build + module caches before actions/cache saves them.
#
# WHY THIS EXISTS
# ---------------
# GitHub's Actions cache is 10 GB for the WHOLE repository. When one entry grows
# without bound it does not merely waste space — it EVICTS every other entry,
# including the cargo caches that `setup` primes, and every job silently goes
# cache-cold while still reporting green. The symptom is a CI that gets slower
# for no visible reason.
#
# The Go module cache is the entry that grows: example FFI dependencies float on
# `"latest"` rather than pinned versions, so each resolution can add a new module
# version and nothing ever removes the old one. It was measured at 75 GB locally
# — 7.5x the entire repo cache budget.
#
# The cache key only hashes `runtime-go/go.{mod,sum}`, so example dependency
# drift never invalidates the entry either; `restore-keys` then chains one
# ever-growing cache forward indefinitely.
#
# WHAT THIS DOES
# --------------
# Runs as the LAST step of a job, before actions/cache's post-step save. If a
# cache is over budget it is emptied, so the next run re-populates a small one.
# That costs one cold Go build occasionally and keeps the repo cache budget
# intact permanently.
#
# This is a SIZE bound, not a correctness gate: `go clean` only discards
# derived/downloadable data. It never changes a build result, only its cost.
#
# Env:
#   GO_CACHE_BUDGET_MB  per-cache ceiling in MiB (default 2048)
#   GOCACHE / GOMODCACHE  the caches to bound (skipped if unset/absent)
set -euo pipefail

budget_mb="${GO_CACHE_BUDGET_MB:-2048}"

# `go clean` needs a go toolchain; if there is none, report and succeed rather
# than failing a job for a housekeeping step.
if ! command -v go > /dev/null 2>&1; then
  echo "bound-go-cache: no go toolchain on PATH; nothing to bound"
  exit 0
fi

bound_one() {
  local label="$1" dir="$2" clean_flag="$3"
  if [ -z "$dir" ] || [ ! -d "$dir" ]; then
    echo "bound-go-cache: ${label} not present (${dir:-unset}); skipping"
    return 0
  fi
  local size_mb
  size_mb=$(du -sm "$dir" 2> /dev/null | cut -f1)
  : "${size_mb:=0}"
  if [ "$size_mb" -gt "$budget_mb" ]; then
    echo "bound-go-cache: ${label} is ${size_mb} MiB, over the ${budget_mb} MiB budget — pruning"
    # `go clean` is authoritative for these dirs (module cache files are
    # read-only, so `rm -rf` is not a safe substitute).
    go clean "$clean_flag"
    local after
    after=$(du -sm "$dir" 2> /dev/null | cut -f1 || echo 0)
    echo "bound-go-cache: ${label} now ${after:-0} MiB"
  else
    echo "bound-go-cache: ${label} is ${size_mb} MiB, within the ${budget_mb} MiB budget"
  fi
}

bound_one "GOCACHE (build)"     "${GOCACHE:-}"    "-cache"
bound_one "GOMODCACHE (module)" "${GOMODCACHE:-}" "-modcache"
