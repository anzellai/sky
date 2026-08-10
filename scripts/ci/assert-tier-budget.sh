#!/usr/bin/env bash
#
# Assert the T1 tier budget (docs/ci-test-architecture-v2.md §8.2).
#
# WHY PER-JOB `timeout-minutes` IS NOT ENOUGH
# -------------------------------------------
# `timeout-minutes` bounds ONE job. It cannot bound a TIER: two sequential jobs
# at 13.5 min each both pass their own timeout and produce a 26-minute "15-minute
# tier". The tier is a property of the GRAPH, so only a fan-in job can assert it.
#
# THE FORMULA (§8.2)
#     tier = max( setup + max(elapsed of jobs that `needs: setup`),
#                 max(elapsed of jobs that do NOT) )
#     tier <= ceiling * (1 + grace)
#
# `setup` is serialised in front of the jobs that depend on it, so it is
# additive for those; they then run concurrently, so only the slowest counts.
# Jobs that do NOT depend on `setup` (a different-OS runner with its own cache,
# a service-container job with no Rust build) start immediately alongside it, so
# charging them `setup + elapsed` would overstate the tier and could fail a
# budget that was actually met. SETUP_INDEPENDENT_JOBS names them.
#
# JOB ELAPSED, NOT WALL CLOCK. GitHub's job `started_at` is when the job began
# EXECUTING and `created_at` is when it was queued. Queue time is a function of
# runner availability, not of this repository's test design, and §8.2 is explicit
# that it must not be charged to the budget. So this uses
# `completed_at - started_at` per job.
#
# Input: the run's jobs as GitHub's REST payload
# (`GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs`) on stdin or in $1.
# Env: T1_CEILING_SECONDS, T1_GRACE_PERCENT, SETUP_JOB_NAME (default "setup").
#      IGNORE_JOBS — space-separated job names excluded from max() (the fan-in
#      job itself, which is still running and has no completed_at).
#      SETUP_INDEPENDENT_JOBS — space-separated jobs that do not `needs: setup`.
set -euo pipefail

ceiling="${T1_CEILING_SECONDS:?T1_CEILING_SECONDS required}"
grace="${T1_GRACE_PERCENT:-0}"
setup_name="${SETUP_JOB_NAME:-setup}"
ignore="${IGNORE_JOBS:-ci-green}"
independent="${SETUP_INDEPENDENT_JOBS:-}"

payload="${1:-}"
if [ -n "$payload" ] && [ -f "$payload" ]; then
  json=$(cat "$payload")
else
  json=$(cat)
fi

# name<TAB>elapsed_seconds for every job that actually ran to completion.
rows=$(printf '%s' "$json" | jq -r '
  .jobs[]
  | select(.started_at != null and .completed_at != null)
  | [ .name,
      ((.completed_at | fromdateiso8601) - (.started_at | fromdateiso8601))
    ]
  | @tsv
')

if [ -z "$rows" ]; then
  echo "::error::tier budget: no completed jobs in the payload — cannot establish a verdict" >&2
  exit 1
fi

setup_elapsed=""
dep_max=0;   dep_name="(none)"
indep_max=0; indep_name="(none)"

is_in() { # is_in <needle> <space-separated haystack>
  local n="$1" h="$2" x
  # shellcheck disable=SC2086
  for x in $h; do [ "$n" = "$x" ] && return 0; done
  return 1
}

echo "── per-job elapsed (completed_at - started_at) ──"
while IFS=$'\t' read -r name secs; do
  [ -n "$name" ] || continue
  printf '  %-28s %6ss\n' "$name" "$secs"
  is_in "$name" "$ignore" && continue
  if [ "$name" = "$setup_name" ]; then
    setup_elapsed="$secs"
  elif is_in "$name" "$independent"; then
    if [ "$secs" -gt "$indep_max" ]; then indep_max="$secs"; indep_name="$name"; fi
  else
    if [ "$secs" -gt "$dep_max" ]; then dep_max="$secs"; dep_name="$name"; fi
  fi
done <<< "$rows"

if [ -z "$setup_elapsed" ]; then
  # A missing `setup` is a graph change, not a pass. Refuse to guess.
  echo "::error::tier budget: no '${setup_name}' job in the run — the budget formula assumes it is the serialised root" >&2
  exit 1
fi

chain=$(( setup_elapsed + dep_max ))
total="$chain"
if [ "$indep_max" -gt "$total" ]; then total="$indep_max"; fi
allowed=$(( ceiling + (ceiling * grace / 100) ))

echo
echo "  setup                                 ${setup_elapsed}s"
echo "  + slowest setup-dependent (${dep_name})  ${dep_max}s"
echo "  = dependent chain                     ${chain}s"
echo "  slowest setup-independent (${indep_name})  ${indep_max}s"
echo "  --------------------------------------------"
echo "  tier total (max of the two)           ${total}s"
echo "  ceiling ${ceiling}s + ${grace}% grace = ${allowed}s"
echo

if [ "$total" -gt "$allowed" ]; then
  echo "::error::T1 TIER BUDGET EXCEEDED: ${total}s > ${allowed}s (chain: setup ${setup_elapsed}s + ${dep_name} ${dep_max}s; independent: ${indep_name} ${indep_max}s)." >&2
  echo "::error::Fix the critical path. Do NOT raise T1_CEILING_SECONDS — that is the silent budget drift this gate exists to catch." >&2
  exit 1
fi

echo "T1 tier budget OK: ${total}s <= ${allowed}s"
