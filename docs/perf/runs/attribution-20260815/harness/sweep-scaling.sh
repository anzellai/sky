#!/usr/bin/env bash
# Does throughput scale with cores? If a global lock serialised interactions,
# throughput would be flat in GOMAXPROCS regardless of per-interaction cost.
# Run BOTH targets so Sky.Live's scaling is read against a known-good floor.
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/perfbench
OUTROOT="$BASE/runs/scaling"
mkdir -p "$OUTROOT"
: >| "$OUTROOT/scaling.tsv"
printf 'target\tgomaxprocs\trep\tsessions_est\tinteractions\tthroughput\tp50_ms\tp95_ms\tapp_cpu_s\twall_s\tcpu_per_int_ms\tvalid\n' >> "$OUTROOT/scaling.tsv"

for target in sky control; do
  for g in 1 2 4 8; do
    for rep in 1 2 3; do
      o="$OUTROOT/$target-g$g-r$rep"
      rm -rf "$o"
      MODE=cpu TARGET="$target" N=50 THINK=0 DUR=20s RAMP=3s WARMUP=3s \
        GOMAXPROCS_SET="$g" PROFILE=0 REP="$rep" OUT="$o" \
        bash "$BASE/perfrun.sh" >/dev/null 2>&1 || { echo "FAILED $o" >&2; continue; }

      # pull the fields with jq-free awk over the JSON the generator wrote
      read -r est ints thr p50 p95 valid <<EOF
$(awk '
  /"sessions_established"/ {gsub(/[^0-9]/,"",$2); est=$2}
  /"interactions_counted"/ {gsub(/[^0-9]/,"",$2); ints=$2}
  /"interactions_per_sec"/ {gsub(/[^0-9.]/,"",$2); thr=$2}
  /"p50_ms"/ {gsub(/[^0-9.]/,"",$2); p50=$2}
  /"p95_ms"/ {gsub(/[^0-9.]/,"",$2); p95=$2}
  /"valid"/  {v=($2 ~ /true/)?"true":"false"}
  END{print est, ints, thr, p50, p95, v}
' "$o/load.json")
EOF
      cpu=$(awk '/app_cpu_delta_s/{print $2}' "$o/cpu-accounting.txt")
      wall=$(awk '/run_wall_s/{print $2}' "$o/cpu-accounting.txt")
      per=$(awk -v c="$cpu" -v i="$ints" 'BEGIN{if(i>0) printf "%.3f", c*1000/i; else print "NA"}')
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$target" "$g" "$rep" "$est" "$ints" "$thr" "$p50" "$p95" "$cpu" "$wall" "$per" "$valid" \
        >> "$OUTROOT/scaling.tsv"
      echo "$target g=$g rep=$rep -> $thr/s  cpu/int=${per}ms  valid=$valid"
    done
  done
done
echo "--- $OUTROOT/scaling.tsv ---"
cat "$OUTROOT/scaling.tsv"
