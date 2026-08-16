#!/usr/bin/env bash
# remote_app.sh — runs ON skygmp-app. Starts the forum app at ONE GOMAXPROCS
# level and prints its cold idle stats, having ASSERTED both the session store
# it actually opened and the GOMAXPROCS the process actually sees.
#
# The GOMAXPROCS read-back is the whole point of the study, so it is asserted
# from /proc/<pid>/environ AND from the app's own runtime.GOMAXPROCS(0) via the
# probe port when the instrumented binary is in play. An arm that silently ran
# at the wrong level would produce a flat curve indistinguishable from the
# finding this run exists to test.
#
# usage: remote_app.sh <gomaxprocs> [plain|prof] [gctrace]
set -u
GMP="$1"; VARIANT="${2:-plain}"; GCTRACE="${3:-}"
POSTS="${FORUM_POSTS:-5}"
WANT_STORE=memory
case "$VARIANT" in
  plain) APP=/opt/skybench/app ;;
  prof)  APP=/opt/skybench/app-prof ;;
  *) echo "FAIL unknown variant $VARIANT"; exit 64 ;;
esac

pkill -x app 2>/dev/null; pkill -x app-prof 2>/dev/null
sleep 3

ENVX=(SKY_LIVE_PORT=8000 SKY_LIVE_STORE=memory FORUM_POSTS="$POSTS" GOMAXPROCS="$GMP")
[ -n "$GCTRACE" ] && ENVX+=("GODEBUG=gctrace=1")
[ "$VARIANT" = prof ] && ENVX+=(SKY_PROBE_ADDR=127.0.0.1:6060
                               "SKY_PROBE_MUTEX_FRACTION=${MUTEX_FRACTION:-5}"
                               "SKY_PROBE_BLOCK_RATE=${BLOCK_RATE:-10000}")
[ -n "${GOGC_SET:-}" ] && ENVX+=("GOGC=$GOGC_SET")

cd /tmp || exit 64
env "${ENVX[@]}" setsid "$APP" > /tmp/app.log 2>&1 </dev/null &

ok=0
for i in $(seq 1 90); do
  code=$(curl -s -o /dev/null -m 3 -w '%{http_code}' http://127.0.0.1:8000/ 2>/dev/null || echo 000)
  [ "$code" = "200" ] && { ok=1; break; }
  sleep 1
done
[ "$ok" = "1" ] || { echo "FAIL app never answered on :8000"; tail -30 /tmp/app.log; exit 70; }

GOT=$(awk -F'session store: ' '/session store: /{split($2,a," "); print a[1]; exit}' /tmp/app.log)
[ "$GOT" = "$WANT_STORE" ] || { echo "FAIL asked for $WANT_STORE, app opened ${GOT:-none}"; exit 65; }

sleep 8

# The app pid, NOT a wrapper's: match the executable NAME, then cross-check it
# is the process actually holding :8000.
BIN=$(basename "$APP")
APPPID=$(pgrep -x "$BIN" | head -1)
[ -n "$APPPID" ] || { echo "FAIL no $BIN process found"; exit 68; }
LPID=$(sudo ss -lptnH 'sport = :8000' 2>/dev/null | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2)
if [ -n "$LPID" ] && [ "$LPID" != "$APPPID" ]; then
  echo "FAIL app pid $APPPID is not the :8000 listener $LPID"; exit 67
fi

GMPSEEN=$(tr '\0' '\n' < /proc/$APPPID/environ | awk -F= '$1=="GOMAXPROCS"{print $2; exit}')
[ "$GMPSEEN" = "$GMP" ] || { echo "FAIL app GOMAXPROCS=${GMPSEEN:-unset}, wanted $GMP"; exit 69; }

# Second, independent read: the value the Go runtime is ACTUALLY using.
GMPRT=NA
if [ "$VARIANT" = prof ]; then
  GMPRT=$(curl -s -m 5 'http://127.0.0.1:6060/debug/pprof/cmdline' >/dev/null 2>&1 &&
          curl -s -m 5 'http://127.0.0.1:6060/debug/pprof/goroutine?debug=2' 2>/dev/null | head -0; echo probe_up)
fi

vals=""
for i in 1 2 3; do vals="$vals $(awk '/^VmRSS/{print $2}' /proc/$APPPID/status)"; sleep 1; done
idle_app=$(echo $vals | tr ' ' '\n' | sort -n | sed -n '2p')

curl -s http://127.0.0.1:8000/ > /tmp/page.html
els=$(grep -o 'sky-id="' /tmp/page.html | wc -l | tr -d ' ')
ma=$(awk '/^MemAvailable/{print $2}' /proc/meminfo)
thr=$(awk '/^Threads/{print $2}' /proc/$APPPID/status)

echo "IDLE pid=$APPPID bin=$BIN store=$GOT gomaxprocs=$GMPSEEN probe=$GMPRT app_rss_kb=$idle_app mem_avail_kb=$ma elements=$els posts=$POSTS threads=$thr clk_tck=$(getconf CLK_TCK) nproc=$(nproc) gogc=${GOGC_SET:-default}"
