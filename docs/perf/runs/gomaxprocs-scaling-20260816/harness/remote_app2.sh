#!/usr/bin/env bash
# remote_app2.sh — TWO app processes on the one 8-core box, GOMAXPROCS=4 each,
# on ports 8000 and 8001, each with its own private memory session store.
#
# This is the experiment that separates "the hardware runs out" from "one
# process runs out". The box, the binary, the app, the generator and the total
# offered concurrency are all held constant against the single-process
# GOMAXPROCS=8 arm; the ONLY thing that changes is that the work is spread
# across two address spaces, so every process-wide lock, the Go heap and the
# GC pacer are duplicated instead of shared.
set -u
GMP="${1:-4}"
pkill -x app 2>/dev/null; pkill -x app-prof 2>/dev/null; sleep 3
cd /tmp || exit 64
for p in 8000 8001; do
  env SKY_LIVE_PORT=$p SKY_LIVE_STORE=memory FORUM_POSTS=5 GOMAXPROCS="$GMP" \
      setsid /opt/skybench/app > /tmp/app-$p.log 2>&1 </dev/null &
done
for p in 8000 8001; do
  ok=0
  for i in $(seq 1 90); do
    c=$(curl -s -o /dev/null -m 3 -w '%{http_code}' http://127.0.0.1:$p/ 2>/dev/null || echo 000)
    [ "$c" = "200" ] && { ok=1; break; }; sleep 1
  done
  [ "$ok" = 1 ] || { echo "FAIL port $p never answered"; tail -20 /tmp/app-$p.log; exit 70; }
  st=$(awk -F'session store: ' '/session store: /{split($2,a," "); print a[1]; exit}' /tmp/app-$p.log)
  [ "$st" = memory ] || { echo "FAIL port $p opened store $st"; exit 65; }
done
sleep 8
PIDS=$(pgrep -x app | tr '\n' ' ')
n=$(echo $PIDS | wc -w | tr -d ' ')
[ "$n" = 2 ] || { echo "FAIL expected 2 app processes, found $n"; exit 68; }
for pid in $PIDS; do
  gmp=$(tr '\0' '\n' < /proc/$pid/environ | awk -F= '$1=="GOMAXPROCS"{print $2; exit}')
  prt=$(tr '\0' '\n' < /proc/$pid/environ | awk -F= '$1=="SKY_LIVE_PORT"{print $2; exit}')
  [ "$gmp" = "$GMP" ] || { echo "FAIL pid $pid GOMAXPROCS=$gmp"; exit 69; }
  echo "  proc pid=$pid port=$prt gomaxprocs=$gmp"
done
els=$(curl -s http://127.0.0.1:8000/ | grep -o 'sky-id="' | wc -l | tr -d ' ')
echo "IDLE2 pids=[$PIDS] gomaxprocs=$GMP elements=$els nproc=$(nproc)"
