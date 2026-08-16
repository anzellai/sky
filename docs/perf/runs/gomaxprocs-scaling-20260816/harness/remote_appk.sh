#!/usr/bin/env bash
# remote_appk.sh — K app processes on the one box, GOMAXPROCS=G each, ports
# 8000..8000+K-1. Generalises the 2-process experiment.
# usage: remote_appk.sh <k> <gomaxprocs>
set -u
K="$1"; G="$2"
pkill -x app 2>/dev/null; pkill -x app-prof 2>/dev/null; sleep 3
cd /tmp || exit 64
PORTS=""
for i in $(seq 0 $((K-1))); do p=$((8000+i)); PORTS="$PORTS $p"
  env SKY_LIVE_PORT=$p SKY_LIVE_STORE=memory FORUM_POSTS=5 GOMAXPROCS="$G" \
      setsid /opt/skybench/app > /tmp/app-$p.log 2>&1 </dev/null &
done
for p in $PORTS; do
  ok=0; for i in $(seq 1 90); do
    c=$(curl -s -o /dev/null -m 3 -w '%{http_code}' http://127.0.0.1:$p/ 2>/dev/null || echo 000)
    [ "$c" = 200 ] && { ok=1; break; }; sleep 1; done
  [ "$ok" = 1 ] || { echo "FAIL port $p never answered"; tail -10 /tmp/app-$p.log; exit 70; }
  st=$(awk -F'session store: ' '/session store: /{split($2,a," "); print a[1]; exit}' /tmp/app-$p.log)
  [ "$st" = memory ] || { echo "FAIL port $p store=$st"; exit 65; }
done
sleep 8
PIDS=$(pgrep -x app | tr '\n' ' '); n=$(echo $PIDS | wc -w | tr -d ' ')
[ "$n" = "$K" ] || { echo "FAIL expected $K app processes, found $n"; exit 68; }
for pid in $PIDS; do
  gmp=$(tr '\0' '\n' < /proc/$pid/environ | awk -F= '$1=="GOMAXPROCS"{print $2; exit}')
  [ "$gmp" = "$G" ] || { echo "FAIL pid $pid GOMAXPROCS=$gmp wanted $G"; exit 69; }
done
echo "IDLEK k=$K gomaxprocs=$G pids=[$PIDS] ports=[$PORTS] nproc=$(nproc)"
