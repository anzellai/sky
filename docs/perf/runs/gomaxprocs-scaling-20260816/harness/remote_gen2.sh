#!/usr/bin/env bash
# remote_gen2.sh — drive BOTH app processes at once from the generator box.
# usage: remote_gen2.sh <appip> <tag> <sessions_each> <dur> <ramp> <warm>
set -u
APPIP="$1"; TAG="$2"; N="$3"; DUR="$4"; RAMP="$5"; WARM="$6"
GEN=/opt/gen/skyliveload; SETUP=/opt/gen/forum-setup.json
OUT=/tmp/out/$TAG; mkdir -p "$OUT"; ulimit -n 200000
for p in 8000 8001; do
  "$GEN" -url "http://$APPIP:$p" -remote-load -assume-yes -self-check \
     -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' > "$OUT/self-$p.txt" 2>&1 \
     || { echo "REJECT self-check failed on port $p"; tail -10 "$OUT/self-$p.txt"; exit 66; }
done
read -r t0 i0 <<<"$(awk '/^cpu /{idle=$5+$6; tot=0; for(i=2;i<=NF;i++) tot+=$i; print tot, idle}' /proc/stat)"
for p in 8000 8001; do
  "$GEN" -url "http://$APPIP:$p" -remote-load -assume-yes \
    -sessions "$N" -duration ${DUR}s -ramp ${RAMP}s -warmup ${WARM}s -think 0 \
    -max-error-rate 1.0 -min-patch-rate 0.9 \
    -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' \
    -json "$OUT/load-$p.json" -label "$TAG-$p" > "$OUT/gen-$p.txt" 2>&1 &
done
wait
read -r t1 i1 <<<"$(awk '/^cpu /{idle=$5+$6; tot=0; for(i=2;i<=NF;i++) tot+=$i; print tot, idle}' /proc/stat)"
echo "GENBOX_BUSY_PCT=$(awk -v t0=$t0 -v i0=$i0 -v t1=$t1 -v i1=$i1 'BEGIN{dt=t1-t0;di=i1-i0; if(dt>0) printf "%.1f",100*(dt-di)/dt; else print 0}')"
for p in 8000 8001; do
  printf 'PORT%s ' "$p"
  grep -oE '"(interactions_per_sec|p50_ms|error_rate|patch_rate|valid|generator_cpu_percent_of_machine)": *[^,}]*' "$OUT/load-$p.json" | tr '\n' ' '
  echo
done
