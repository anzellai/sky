#!/usr/bin/env bash
# remote_run.sh — one measurement repeat, entirely ON skyperf-core.
#
# The generator drives the app over LOOPBACK from the same box, which is what
# the M1 reference run did; the point of an e2-standard-4 is that the app's one
# GOMAXPROCS=1 core and the generator's threads are DEDICATED vCPUs, so neither
# is a burst-credit artefact of the other.
#
# usage: remote_run.sh <tag>
set -u
TAG="$1"
GEN=/opt/gen/skyliveload
SETUP=/opt/gen/forum-setup.json
SESSIONS="${SESSIONS:-25}"
DUR="${DUR:-45}"
RAMP="${RAMP:-3}"
WARM="${WARM:-3}"
THINK="${THINK:-0}"
OUT=/tmp/out/$TAG
mkdir -p "$OUT"

ulimit -n 65535

APPPID=$(pgrep -x app | head -1)
[ -n "$APPPID" ] || { echo "FAIL no app process"; exit 68; }

jiff() { sed 's/.*) //' /proc/$1/stat | awk '{print $12+$13}'; }   # utime+stime
rss()  { awk '/^VmRSS/{print $2}' /proc/$1/status; }

# ---- 1. PRECONDITION: this handler patches on every press ----------------
# Runs BEFORE the measurement window, against the same app, with the same
# setup/selector. A failure REJECTS the repeat; it is never recorded.
if ! "$GEN" -url http://127.0.0.1:8000 -assume-yes -self-check \
      -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' \
      > "$OUT/selfcheck.txt" 2>&1; then
  echo "REJECT self-check failed for $TAG"
  tail -20 "$OUT/selfcheck.txt"
  exit 66
fi

# ---- 2. sampler: RSS + cumulative CPU jiffies, wall-clock stamped --------
SECS=$(( RAMP + WARM + DUR + 30 ))
( end=$(( $(date +%s) + SECS ))
  while [ "$(date +%s)" -lt "$end" ]; do
    [ -r /proc/$APPPID/stat ] || break
    printf '%s\t%s\t%s\n' "$(date +%s.%N)" "$(rss $APPPID)" "$(jiff $APPPID)"
    sleep 0.25
  done ) > "$OUT/sample.tsv" 2>/dev/null &
SPID=$!
sleep 1

# ---- 3. the load ---------------------------------------------------------
GEN_RC=0
"$GEN" -url http://127.0.0.1:8000 -assume-yes \
   -sessions "$SESSIONS" -duration ${DUR}s -ramp ${RAMP}s -warmup ${WARM}s -think "$THINK" \
   -max-error-rate 1.0 -min-patch-rate 0.9 \
   -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' \
   -json "$OUT/load.json" -label "$TAG" > "$OUT/gen.txt" 2>&1 || GEN_RC=$?

# Keep sampling past the generator's exit. The window this is aligned against
# is [started_at + warmup, + duration] and `started_at` is RFC3339 to the
# SECOND, so the true deadline can sit up to a second beyond the generator's
# own exit. Killing the sampler the instant the generator returned left 0.46 s
# of tail margin on the first trial run — enough to clip a real edge and shave
# ~1% off a 45 s window without anything looking wrong.
sleep 4
kill $SPID 2>/dev/null; wait $SPID 2>/dev/null

echo "GEN_RC=$GEN_RC"
echo "APPPID=$APPPID"
echo "CLK_TCK=$(getconf CLK_TCK)"
cat "$OUT/load.json"
