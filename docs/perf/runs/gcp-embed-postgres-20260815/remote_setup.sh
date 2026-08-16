#!/bin/bash
# usage: remote_setup.sh <A|B|C>   -> restarts app in that config, prints cold idle stats
set -u
CFG="$1"
SOCK=/tmp/sky-b5c107ad4fbf1900
case "$CFG" in
  A) sudo tee /etc/skybench.env >/dev/null <<'E'
SKYBENCH_ARGS=
E
;;
  B) sudo tee /etc/skybench.env >/dev/null <<'E'
SKYBENCH_ARGS=--embed --data-dir /var/lib/skybench/data
SKY_POSTGRES_BIN=/usr/lib/postgresql/15/bin
E
;;
  C) sudo tee /etc/skybench.env >/dev/null <<'E'
SKYBENCH_ARGS=--embed --data-dir /var/lib/skybench/data
SKY_POSTGRES_BIN=/usr/lib/postgresql/15/bin
SKY_LIVE_STORE=postgres
E
;;
esac
sudo systemctl daemon-reload
sudo systemctl restart skybench
# wait for HTTP readiness rather than a fixed sleep
for i in $(seq 1 60); do
  code=$(curl -s -o /dev/null -m 3 -w '%{http_code}' http://127.0.0.1:8000/_sky/healthz 2>/dev/null || echo 000)
  [ "$code" = "200" ] && break
  sleep 1
done
sleep 12   # settle: let the go heap and pg aux processes reach steady state
APPPID=$(pgrep -f '/opt/skybench/app' | head -1)
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
store=$(sudo journalctl -u skybench --no-pager _PID=$APPPID | grep -o 'session store: [a-z]*' | head -1)
echo "IDLE cfg=$CFG app_rss_kb=$idle_app app_pss_kb=$app_pss pg_rss_kb=$pg_rss pg_pss_kb=$pg_pss pg_nproc=$pg_n mem_avail_kb=$ma store=[$store]"
