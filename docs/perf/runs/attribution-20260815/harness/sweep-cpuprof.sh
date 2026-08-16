#!/usr/bin/env bash
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/perfbench
for rep in 1 2 3; do
  o="$BASE/runs/cpuprof/closed-r$rep"; rm -rf "$o"
  MODE=cpu TARGET=sky N=50 THINK=0 DUR=45s RAMP=3s WARMUP=3s GOMAXPROCS_SET=1 \
    PROFILE=1 PROFSECS=25 PROFDELAY=12 REP="$rep" OUT="$o" bash "$BASE/perfrun.sh" >/dev/null 2>&1 \
    && echo "closed r$rep ok" || echo "closed r$rep FAILED"
done
# open loop at a realistic think time, to check the breakdown is not an
# artifact of closed-loop saturation
for rep in 1 2; do
  o="$BASE/runs/cpuprof/open-r$rep"; rm -rf "$o"
  MODE=cpu TARGET=sky N=25 THINK=1s DUR=60s RAMP=5s WARMUP=5s GOMAXPROCS_SET=1 \
    PROFILE=1 PROFSECS=30 PROFDELAY=15 REP="$rep" OUT="$o" bash "$BASE/perfrun.sh" >/dev/null 2>&1 \
    && echo "open r$rep ok" || echo "open r$rep FAILED"
done
