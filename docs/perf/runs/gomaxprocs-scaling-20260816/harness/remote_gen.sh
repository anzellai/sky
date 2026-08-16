#!/usr/bin/env bash
# remote_gen.sh — one measurement window, driven ENTIRELY from skygmp-gen.
#
# The generator lives on its own 8-vCPU box so that it cannot take cores away
# from the app at the very arm (GOMAXPROCS=8) where that would manufacture the
# sub-linear result the study is testing for. Its own CPU is measured two ways
# here — the tool's getrusage self-accounting AND the box's /proc/stat busy
# fraction — because "the generator was not the bottleneck" is a claim that has
# to be shown, not asserted.
#
# usage: remote_gen.sh <appip> <tag> <sessions> <dur> <ramp> <warm>
set -u
APPIP="$1"; TAG="$2"; N="$3"; DUR="$4"; RAMP="$5"; WARM="$6"
GEN=/opt/gen/skyliveload
SETUP=/opt/gen/forum-setup.json
OUT=/tmp/out/$TAG
mkdir -p "$OUT"
ulimit -n 200000

# ---- PRECONDITION: this handler patches on every press -------------------
if ! "$GEN" -url "http://$APPIP:8000" -remote-load -assume-yes -self-check \
      -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' \
      > "$OUT/selfcheck.txt" 2>&1; then
  echo "REJECT self-check failed for $TAG"; tail -20 "$OUT/selfcheck.txt"; exit 66
fi

# generator-box CPU across exactly the load, from /proc/stat
read -r t0 i0 <<<"$(awk '/^cpu /{idle=$5+$6; tot=0; for(i=2;i<=NF;i++) tot+=$i; print tot, idle}' /proc/stat)"
s0=$(date +%s)

GEN_RC=0
"$GEN" -url "http://$APPIP:8000" -remote-load -assume-yes \
   -sessions "$N" -duration ${DUR}s -ramp ${RAMP}s -warmup ${WARM}s -think 0 \
   -max-error-rate 1.0 -min-patch-rate 0.9 \
   -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' \
   -json "$OUT/load.json" -label "$TAG" > "$OUT/gen.txt" 2>&1 || GEN_RC=$?

read -r t1 i1 <<<"$(awk '/^cpu /{idle=$5+$6; tot=0; for(i=2;i<=NF;i++) tot+=$i; print tot, idle}' /proc/stat)"
s1=$(date +%s)
GENBOX=$(awk -v t0="$t0" -v i0="$i0" -v t1="$t1" -v i1="$i1" \
  'BEGIN{dt=t1-t0; di=i1-i0; if(dt>0) printf "%.1f", 100*(dt-di)/dt; else printf "0"}')

echo "GEN_RC=$GEN_RC"
echo "GENBOX_BUSY_PCT=$GENBOX  window_s=$((s1-s0))  nproc=$(nproc)"
cat "$OUT/load.json"
