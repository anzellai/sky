#!/usr/bin/env bash
#
# Reclaim runner disk ONLY when the job would otherwise start short of it, and
# never for longer than a fixed budget.
#
# WHY THIS EXISTS
# ---------------
# The release gate jobs (via .github/actions/gate-setup) used to run, on every
# job, unconditionally:
#
#     sudo rm -rf /usr/local/lib/android /usr/share/dotnet /opt/ghc ... || true
#     sudo docker image prune --all --force || true
#
# Measured over 165 gate jobs in release runs 37545837777, 37594890617,
# 37713125127 and 37752520304: median 116 s, p90 165 s, and once 898 s. That
# 15-minute stall (gate-core-sky-4, run 37752520304, the v0.27.8 tag) ate half
# a 30-minute job budget and the job was cancelled with its tests still
# running. Every one of those jobs started with 86 GB free and finished the
# reclaim at 114-115 GB free; `docker image prune` freed only 1.85 GB of that.
#
# 86 GB free is enough without any reclaim: nightly-sweep.yml's heaviest jobs
# (the full clean-slate example sweep, behaviour-corpus, falsifier-verification
# --all, harness-t3) and every rust-ci.yml job run on the same ubuntu-latest
# image with NO reclaim step, and they pass. So the default here is to measure,
# find enough space, and do nothing.
#
# WHAT THIS DOES
# --------------
# 1. Reads free space on /. At or above MIN_FREE_GB (default 80, i.e. the
#    86 GB the unreclaimed runners above are proven to be enough with, less a
#    margin) it logs that and exits: no rm, no docker.
# 2. Below it, removes unused preinstalled toolchains one at a time, cheapest
#    first, each under with_timeout, stopping as soon as MIN_FREE_GB is met or
#    the RECLAIM_BUDGET_S wall-clock budget (default 180) is spent. Each removal
#    logs its duration and the free space after it. `docker image prune` is
#    gone: it freed ~1.85 GB for a slow, unbounded daemon call.
# 3. If the job still starts below MIN_FREE_GB, it emits a ::warning:: naming
#    the shortfall rather than failing: a job that needs less still passes, and
#    one that later hits "no space left on device" has the cause annotated on
#    the run. It never fails the job, so a slow runner costs at most the budget.
#
# Env:
#   MIN_FREE_GB        free GB on / the job should start with (default 80)
#   RECLAIM_BUDGET_S   total wall-clock budget for removals (default 180)
set -uo pipefail

# Runner-only. This script deletes system directories with sudo, so it refuses
# to act anywhere but an ephemeral GitHub-hosted Linux runner. A developer
# machine (where, for example, /usr/share/swift is a real toolchain) gets a
# no-op, whatever its free space. Set by the Actions runner, never by a person.
if [ "${GITHUB_ACTIONS:-}" != "true" ] || [ "${RUNNER_ENVIRONMENT:-}" != "github-hosted" ] || [ "$(uname -s)" != "Linux" ]; then
    echo "reclaim-runner-disk: not a GitHub-hosted Linux runner; nothing removed"
    exit 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/lib/with-timeout.sh
source "$ROOT/scripts/lib/with-timeout.sh"

min_free_gb="${MIN_FREE_GB:-80}"
budget_s="${RECLAIM_BUDGET_S:-180}"

free_gb() { df -Pk / | awk 'NR==2 {print int($4 / 1048576)}'; }

start_free="$(free_gb)"
echo "reclaim-runner-disk: ${start_free} GB free on / (want ${min_free_gb})"
df -h / | tail -1

if [ "$start_free" -ge "$min_free_gb" ]; then
  echo "reclaim-runner-disk: enough free space; nothing removed"
  exit 0
fi

# Unused preinstalled trees, cheapest (fewest files per GB) first. The Android
# SDK is the largest but also the most files, so the slowest to delete: last.
candidates=(
  /opt/hostedtoolcache/CodeQL
  /usr/share/dotnet
  /usr/local/.ghcup
  /opt/ghc
  /usr/share/swift
  /usr/local/share/powershell
  /usr/local/share/boost
  /usr/local/lib/android
)

started=$SECONDS
for dir in "${candidates[@]}"; do
  [ -d "$dir" ] || continue
  left=$((budget_s - (SECONDS - started)))
  if [ "$left" -le 0 ]; then
    echo "reclaim-runner-disk: ${budget_s}s budget spent; stopping before ${dir}"
    break
  fi
  t0=$SECONDS
  rc=0
  with_timeout "$left" sudo -n rm -rf "$dir" || rc=$?
  now_free="$(free_gb)"
  echo "reclaim-runner-disk: rm ${dir}: $((SECONDS - t0))s, exit ${rc}, ${now_free} GB free"
  if [ "$now_free" -ge "$min_free_gb" ]; then
    break
  fi
done

end_free="$(free_gb)"
echo "reclaim-runner-disk: ${start_free} -> ${end_free} GB free in $((SECONDS - started))s"
df -h / | tail -1
if [ "$end_free" -lt "$min_free_gb" ]; then
  echo "::warning title=Runner disk below target::this job starts with ${end_free} GB free on /, under the ${min_free_gb} GB the release gates are sized for (reclaim stopped after $((SECONDS - started))s of a ${budget_s}s budget). A later 'no space left on device' in this job is this shortfall."
fi
exit 0
