#!/usr/bin/env bash
# usage: remote_genk.sh <appip> <tag> <k> <sessions_each> <dur> <ramp> <warm>
set -u
APPIP="$1"; TAG="$2"; K="$3"; N="$4"; DUR="$5"; RAMP="$6"; WARM="$7"
GEN=/opt/gen/skyliveload; SETUP=/opt/gen/forum-setup.json
OUT=/tmp/out/$TAG; mkdir -p "$OUT"; ulimit -n 200000
PORTS=$(for i in $(seq 0 $((K-1))); do echo $((8000+i)); done)
for p in $PORTS; do
  "$GEN" -url "http://$APPIP:$p" -remote-load -assume-yes -self-check \
     -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' > "$OUT/self-$p.txt" 2>&1 \
     || { echo "REJECT self-check port $p"; tail -8 "$OUT/self-$p.txt"; exit 66; }
done
read -r t0 i0 <<<"$(awk '/^cpu /{idle=$5+$6;tot=0;for(i=2;i<=NF;i++)tot+=$i;print tot,idle}' /proc/stat)"
for p in $PORTS; do
  "$GEN" -url "http://$APPIP:$p" -remote-load -assume-yes \
    -sessions "$N" -duration ${DUR}s -ramp ${RAMP}s -warmup ${WARM}s -think 0 \
    -max-error-rate 1.0 -min-patch-rate 0.9 \
    -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' \
    -json "$OUT/load-$p.json" -label "$TAG-$p" > "$OUT/gen-$p.txt" 2>&1 &
done
wait
read -r t1 i1 <<<"$(awk '/^cpu /{idle=$5+$6;tot=0;for(i=2;i<=NF;i++)tot+=$i;print tot,idle}' /proc/stat)"
echo "GENBOX_BUSY_PCT=$(awk -v t0=$t0 -v i0=$i0 -v t1=$t1 -v i1=$i1 'BEGIN{dt=t1-t0;di=i1-i0;if(dt>0)printf "%.1f",100*(dt-di)/dt;else print 0}')"
TOT=0
for p in $PORTS; do
  v=$(grep -o '"interactions_per_sec": *[0-9.]*' "$OUT/load-$p.json" | head -1 | sed 's/.*: *//')
  vd=$(grep -o '"valid": *[a-z]*' "$OUT/load-$p.json" | head -1 | sed 's/.*: *//')
  pr=$(grep -o '"patch_rate": *[0-9.]*' "$OUT/load-$p.json" | head -1 | sed 's/.*: *//')
  echo "  port $p  tput=$v valid=$vd patch=$pr"
  TOT=$(awk -v a="$TOT" -v b="$v" 'BEGIN{print a+b}')
done
echo "AGGREGATE_TPUT=$TOT"
