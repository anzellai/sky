#!/usr/bin/env bash
# remote_app_pin.sh — one app at GOMAXPROCS=G pinned to an explicit CPU set.
#
# This is what separates "GOMAXPROCS=8 stopped scaling because the application
# stopped scaling" from "GOMAXPROCS=8 stopped scaling because vCPUs 4-7 are the
# SMT siblings of 0-3 and there were never 8 cores". Same G, same offered load,
# same binary; only WHICH logical cpus the process may run on changes:
#   0,1,2,3  -> four DISTINCT physical cores
#   0,4,1,5  -> two physical cores, each with both its threads
# usage: remote_app_pin.sh <gomaxprocs> <cpulist>
set -u
G="$1"; CPUS="$2"
pkill -x app 2>/dev/null; pkill -x app-prof 2>/dev/null; sleep 3
cd /tmp || exit 64
env SKY_LIVE_PORT=8000 SKY_LIVE_STORE=memory FORUM_POSTS=5 GOMAXPROCS="$G" \
    setsid taskset -c "$CPUS" /opt/skybench/app > /tmp/app.log 2>&1 </dev/null &
ok=0; for i in $(seq 1 90); do
  c=$(curl -s -o /dev/null -m 3 -w '%{http_code}' http://127.0.0.1:8000/ 2>/dev/null || echo 000)
  [ "$c" = 200 ] && { ok=1; break; }; sleep 1; done
[ "$ok" = 1 ] || { echo "FAIL no answer"; tail -10 /tmp/app.log; exit 70; }
st=$(awk -F'session store: ' '/session store: /{split($2,a," "); print a[1]; exit}' /tmp/app.log)
[ "$st" = memory ] || { echo "FAIL store=$st"; exit 65; }
sleep 8
PID=$(pgrep -x app | head -1); [ -n "$PID" ] || { echo "FAIL no pid"; exit 68; }
LPID=$(sudo ss -lptnH 'sport = :8000' 2>/dev/null | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2)
[ -z "$LPID" ] || [ "$LPID" = "$PID" ] || { echo "FAIL pid $PID is not listener $LPID"; exit 67; }
GOT=$(taskset -pc "$PID" 2>/dev/null | sed 's/.*: //')
GMP=$(tr '\0' '\n' < /proc/$PID/environ | awk -F= '$1=="GOMAXPROCS"{print $2; exit}')
[ "$GMP" = "$G" ] || { echo "FAIL gomaxprocs=$GMP"; exit 69; }
echo "IDLEPIN pid=$PID gomaxprocs=$GMP affinity=$GOT wanted=$CPUS"
