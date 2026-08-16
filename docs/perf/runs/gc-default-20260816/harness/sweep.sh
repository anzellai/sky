#!/usr/bin/env bash
# Wait for the host to be quiet, then run the arms.
#
# The wait is not politeness, it is validity: LOCKS.md in the parent run
# records a 24–44% within-arm throughput spread measured with sibling agents
# active, against a prize the profile bounded at 4.7%. RSS is the robust
# quantity here (≤6% spread at GOGC ≤ 400 in the parent run) but session
# ESTABLISHMENT is not — a saturated host fails to establish, and an arm that
# established 380 of 500 measures a different workload.
set -u
MB=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gcdef
BIN="$MB/bin/forumbench-gcdefault"
LOADCAP=${LOADCAP:-10}

load1() { uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/'; }
waitquiet() {
  for i in $(seq 1 180); do   # up to 60 min
    l=$(load1)
    if [ "$(printf '%.0f' "$l")" -le "$LOADCAP" ]; then echo "host quiet: load1=$l"; return 0; fi
    [ $((i % 15)) -eq 0 ] && echo "waiting for host: load1=$l (${i}0s)"
    sleep 20
  done
  echo "host never went quiet; proceeding and recording load1 per arm"
  return 0
}

run() { echo "--- $* ---"; waitquiet; "$MB/runone.sh" "$@"; }

# A. The shipped default, nothing in the environment. On this 16GB host the
#    derived limit is ~9.7GB and will NOT bind — that is the correct outcome
#    for a 16GB machine and it is what the arm records.
for n in 100 300 500; do run "default-n$n" "$n" "$BIN"; done

# B. The e2-small simulation: the EXACT figures the rule derives for a
#    1.93GiB instance running --embed (996MB limit, GOGC=400), supplied through
#    the environment because this host is not that machine. The derivation
#    itself is proven by unit test; this arm proves the bound HOLDS.
for n in 100 300 500; do run "e2small-n$n" "$n" "$BIN" 400 996MiB; done

# C. The falsifier the parent run named and did not test: a live heap that
#    exceeds the bound. 192MiB is below the app's own measured working set at
#    every one of these session counts, so the collector is running against a
#    target it cannot reach. The property under test is that this DEGRADES
#    rather than dying — the process serves, RSS exceeds the soft limit rather
#    than the process being killed, and no arm aborts.
for n in 300 500; do run "overbound-n$n" "$n" "$BIN" 400 192MiB; done

echo "=== sweep complete ==="
for f in "$MB"/runs/*/acct.txt; do
  echo "--- $(dirname "$f" | xargs basename)"; cat "$f"
done
