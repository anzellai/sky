#!/usr/bin/env bash
# remote_app.sh — runs ON A TARGET. Restarts the app in one configuration and
# prints its cold idle stats, having ASSERTED the session store it actually
# opened.
#
# The store assertion is the whole reason this is a script rather than a
# command line. Sky.Live's dev fallback degrades an unreachable durable store
# to memory and then serves every request correctly, so a mis-configured run
# is valid, patch-bearing, repeatable — and about a different system. It has
# to abort here, before the measurement window opens.
#
# usage: remote_app.sh <mem|pg|pgnofsync>
set -u
CFG="$1"
APP=/opt/skybench/app
DATA=/var/lib/skybench/data
PGBIN=/usr/lib/postgresql/15/bin
FORUM_POSTS="${FORUM_POSTS:-5}"

sudo pkill -f "$APP" 2>/dev/null
sudo pkill -x postgres 2>/dev/null
sleep 3

case "$CFG" in
  mem)
    WANT_STORE=memory
    sudo -u skybench env \
      SKY_LIVE_PORT=8000 SKY_LIVE_STORE=memory FORUM_POSTS="$FORUM_POSTS" \
      SKY_PERF_PPROF_ADDR=127.0.0.1:6577 \
      setsid "$APP" > /tmp/app.log 2>&1 </dev/null &
    ;;
  pg|pgnofsync)
    WANT_STORE=postgres
    sudo -u skybench env \
      SKY_LIVE_PORT=8000 SKY_LIVE_STORE=postgres FORUM_POSTS="$FORUM_POSTS" \
      SKY_POSTGRES_BIN="$PGBIN" \
      SKY_PERF_PPROF_ADDR=127.0.0.1:6577 \
      setsid "$APP" --embed --data-dir "$DATA" > /tmp/app.log 2>&1 </dev/null &
    ;;
  *) echo "FAIL unknown cfg $CFG"; exit 64;;
esac

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

# fsync=off variant: flip it on the RUNNING embedded cluster, then prove it
# took. This exists only as a contrast arm — it is never a recommendation.
if [ "$CFG" = "pgnofsync" ]; then
  SOCK=$(ls -d /tmp/sky-* 2>/dev/null | head -1)
  sudo -u skybench "$PGBIN/psql" -h "$SOCK" -d postgres -Atc \
     "ALTER SYSTEM SET fsync=off; ALTER SYSTEM SET synchronous_commit=off; SELECT pg_reload_conf();" >/dev/null 2>&1
  FS=$(sudo -u skybench "$PGBIN/psql" -h "$SOCK" -d postgres -Atc "show fsync" 2>/dev/null)
  SC=$(sudo -u skybench "$PGBIN/psql" -h "$SOCK" -d postgres -Atc "show synchronous_commit" 2>/dev/null)
  if [ "$FS" != "off" ] || [ "$SC" != "off" ]; then
    echo "FAIL pgnofsync arm: fsync=$FS synchronous_commit=$SC (wanted off/off)"; exit 66
  fi
fi

sleep 10   # settle: go heap + pg aux processes reach steady state

# The app pid, NOT the sudo wrapper's.
#
# `pgrep -f /opt/skybench/app | head -1` matches TWO processes here — the
# `sudo -u skybench env … /opt/skybench/app` wrapper and the app itself — and
# head -1 takes the lower pid, which is the wrapper. Measured on this box:
# wrapper comm=sudo rss=11956 jiffies=1; app comm=app rss=23400. Every RSS in
# a sweep built on that pattern is the wrapper's ~12 MB constant, and every
# CPU delta is ~0. `pgrep -x app` matches the executable NAME and yields
# exactly one pid; cross-checked against the :8000 listener pid from ss.
APPPID=$(pgrep -x app | head -1)
LPID=$(sudo ss -lptnH 'sport = :8000' 2>/dev/null | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2)
if [ -n "$LPID" ] && [ "$LPID" != "$APPPID" ]; then
  echo "FAIL app pid $APPPID is not the :8000 listener $LPID"; exit 67
fi
[ -n "$APPPID" ] || { echo "FAIL no app process found"; exit 68; }
vals=""
for i in 1 2 3 4 5; do
  vals="$vals $(awk '/^VmRSS/{print $2}' /proc/$APPPID/status)"; sleep 1
done
idle_app=$(echo $vals | tr ' ' '\n' | sort -n | sed -n '3p')

pg_rss=0; pg_n=0; pg_pss=0
for p in $(pgrep -x postgres 2>/dev/null); do
  r=$(awk '/^VmRSS/{print $2}' /proc/$p/status 2>/dev/null || echo 0)
  s=$(sudo awk '/^Pss:/{s+=$2} END{print s+0}' /proc/$p/smaps_rollup 2>/dev/null || echo 0)
  pg_rss=$((pg_rss+r)); pg_pss=$((pg_pss+s)); pg_n=$((pg_n+1))
done
app_pss=$(sudo awk '/^Pss:/{s+=$2} END{print s+0}' /proc/$APPPID/smaps_rollup 2>/dev/null || echo 0)
ma=$(awk '/^MemAvailable/{print $2}' /proc/meminfo)

# The view size, counted from the HTML the app ACTUALLY served, never from an
# expectation.
curl -s http://127.0.0.1:8000/ > /tmp/page.html
els=$(grep -o 'sky-id="' /tmp/page.html | wc -l | tr -d ' ')

fsyncv=NA; sbuf=NA; mconn=NA
if [ "$CFG" != "mem" ]; then
  SOCK=$(ls -d /tmp/sky-* 2>/dev/null | head -1)
  fsyncv=$(sudo -u skybench "$PGBIN/psql" -h "$SOCK" -d postgres -Atc "show fsync" 2>/dev/null || echo ERR)
  sbuf=$(sudo -u skybench "$PGBIN/psql" -h "$SOCK" -d postgres -Atc "show shared_buffers" 2>/dev/null || echo ERR)
  mconn=$(sudo -u skybench "$PGBIN/psql" -h "$SOCK" -d postgres -Atc "show max_connections" 2>/dev/null || echo ERR)
fi

echo "IDLE cfg=$CFG store=$GOT app_rss_kb=$idle_app app_pss_kb=$app_pss pg_rss_kb=$pg_rss pg_pss_kb=$pg_pss pg_nproc=$pg_n mem_avail_kb=$ma elements=$els posts=$FORUM_POSTS fsync=$fsyncv shared_buffers=$sbuf max_connections=$mconn"
