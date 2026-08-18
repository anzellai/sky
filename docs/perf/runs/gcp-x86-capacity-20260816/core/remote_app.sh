#!/usr/bin/env bash
# remote_app.sh — runs ON skyperf-core. Starts forumbench in the single
# configuration this study measures and prints its cold idle stats, having
# ASSERTED the session store it actually opened and COUNTED the view size
# from the HTML it actually served.
#
# The store assertion is the whole reason this is a script rather than a
# command line: Sky.Live's dev fallback degrades an unreachable durable store
# to memory and serves every request correctly afterwards, so a mis-configured
# run is valid, patch-bearing, repeatable — and about a different system.
#
# usage: remote_app.sh [posts]
set -u
APP=/opt/skybench/app
POSTS="${1:-5}"
WANT_STORE=memory

pkill -x app 2>/dev/null
sleep 3

cd /tmp || exit 64
env SKY_LIVE_PORT=8000 SKY_LIVE_STORE=memory FORUM_POSTS="$POSTS" GOMAXPROCS=1 \
    setsid "$APP" > /tmp/app.log 2>&1 </dev/null &

# HTTP readiness, not a fixed sleep.
ok=0
for i in $(seq 1 90); do
  code=$(curl -s -o /dev/null -m 3 -w '%{http_code}' http://127.0.0.1:8000/ 2>/dev/null || echo 000)
  [ "$code" = "200" ] && { ok=1; break; }
  sleep 1
done
[ "$ok" = "1" ] || { echo "FAIL app never answered on :8000"; tail -30 /tmp/app.log; exit 70; }

# ---- the store assertion -------------------------------------------------
GOT=$(awk -F'session store: ' '/session store: /{split($2,a," "); print a[1]; exit}' /tmp/app.log)
if [ "$GOT" != "$WANT_STORE" ]; then
  echo "FAIL asked for $WANT_STORE, app opened ${GOT:-none}"
  grep -i store /tmp/app.log | head -20
  exit 65
fi

sleep 10   # settle: go heap reaches steady state

# The app pid, NOT a wrapper's. `pgrep -f <path> | head -1` also matches any
# wrapper carrying the path in its argv and returns the LOWER pid, i.e. the
# wrapper — whose RSS is a ~12 MB constant and whose CPU delta is ~0. Match the
# executable NAME, then cross-check it is the process holding :8000.
APPPID=$(pgrep -x app | head -1)
[ -n "$APPPID" ] || { echo "FAIL no app process found"; exit 68; }
LPID=$(sudo ss -lptnH 'sport = :8000' 2>/dev/null | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2)
if [ -n "$LPID" ] && [ "$LPID" != "$APPPID" ]; then
  echo "FAIL app pid $APPPID is not the :8000 listener $LPID"; exit 67
fi

# GOMAXPROCS as the process ACTUALLY sees it, from its own environ.
GMP=$(tr '\0' '\n' < /proc/$APPPID/environ | awk -F= '$1=="GOMAXPROCS"{print $2; exit}')
[ "$GMP" = "1" ] || { echo "FAIL app GOMAXPROCS=${GMP:-unset}, wanted 1"; exit 69; }

vals=""
for i in 1 2 3 4 5; do
  vals="$vals $(awk '/^VmRSS/{print $2}' /proc/$APPPID/status)"; sleep 1
done
idle_app=$(echo $vals | tr ' ' '\n' | sort -n | sed -n '3p')

# The view size, counted from the HTML the app ACTUALLY served, never assumed.
curl -s http://127.0.0.1:8000/ > /tmp/page.html
els=$(grep -o 'sky-id="' /tmp/page.html | wc -l | tr -d ' ')

ma=$(awk '/^MemAvailable/{print $2}' /proc/meminfo)
echo "IDLE pid=$APPPID store=$GOT gomaxprocs=$GMP app_rss_kb=$idle_app mem_avail_kb=$ma elements=$els posts=$POSTS clk_tck=$(getconf CLK_TCK) nproc=$(nproc)"
