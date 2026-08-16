#!/usr/bin/env bash
# wait-quiet.sh — block until the host is quiet enough to measure on.
#
# A measurement started next to a concurrent benchmark is not a measurement, and
# the failure is INVISIBLE in the result: a contaminated throughput number looks
# exactly like a real one. This run set lost four 974-element runs that way when
# a sibling agent's benchmark went from idle to 590% CPU mid-window.
#
# "Quiet" is deliberately conservative on two axes, because a sibling A/B is a
# SEQUENCE of runs with idle gaps between them — a single instantaneous check
# lands in a gap and reports quiet:
#   * no sibling driver process alive, AND
#   * no non-mine process above CPU_MAX, AND
#   * both true continuously for HOLD seconds.
set -euo pipefail
HOLD="${HOLD:-120}"
CPU_MAX="${CPU_MAX:-40}"
MINE="${MINE:-stage4}"
TIMEOUT="${TIMEOUT:-5400}"

start=$(date +%s)
streak=0
while :; do
  now=$(date +%s)
  if [ $((now - start)) -gt "$TIMEOUT" ]; then
    echo "wait-quiet: TIMED OUT after ${TIMEOUT}s — host never went quiet" >&2
    exit 75
  fi
  # Drivers of a sibling measurement, by scratchpad subdirectory (never by
  # process NAME: siblings run binaries called `app-probe` and `skyliveload`
  # too, and matching those would be indistinguishable from matching my own).
  sib=$(ps -Ao command | grep -E "scratchpad/[a-z0-9-]+/(ab[0-9]*\.sh|bin/)" | grep -v "scratchpad/$MINE/" | grep -vc grep || true)
  # Only OTHER AGENTS' work counts as contention, and every agent on this host
  # runs out of the shared scratchpad. Keying on "any user process above N%"
  # instead was tried and never released: Google Drive spikes past 40% every
  # minute or so, resetting the streak forever — and it was equally present
  # during the clean runs, so it is background, not contention.
  hot=$(ps -Ao pcpu,command | awk -v m="$CPU_MAX" -v mine="scratchpad/$MINE/" -v sp="scratchpad/" '
          $1 > m && index($0, sp) > 0 && index($0, mine) == 0 { n++ } END { print n+0 }')
  if [ "$sib" -eq 0 ] && [ "$hot" -eq 0 ]; then
    streak=$((streak + 10))
    if [ "$streak" -ge "$HOLD" ]; then
      echo "wait-quiet: host quiet for ${HOLD}s (load $(uptime | sed -E 's/.*averages?: //'))"
      exit 0
    fi
  else
    [ "$streak" -gt 0 ] && echo "wait-quiet: busy again (sib=$sib hot=$hot) — streak reset" >&2
    streak=0
  fi
  sleep 10
done
