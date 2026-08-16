#!/usr/bin/env bash
# remote_sampler.sh — 1 Hz trace on the app box while the load runs.
# usage: remote_sampler.sh <seconds> <outfile> <binname>
# columns: epoch app_rss_kb mem_avail_kb conn8000 cpu_total_j cpu_idle_j app_j threads
SECS="${1:-120}"; OUT="${2:-/tmp/s.tsv}"; BIN="${3:-app}"
: > "$OUT"
for i in $(seq 1 "$SECS"); do
  APPPID=$(pgrep -x "$BIN" | head -1)
  app_rss=0; app_j=0; thr=0
  if [ -n "$APPPID" ] && [ -d "/proc/$APPPID" ]; then
    app_rss=$(awk '/^VmRSS/{print $2}' /proc/$APPPID/status 2>/dev/null || echo 0)
    app_j=$(awk '{print $14+$15}' /proc/$APPPID/stat 2>/dev/null || echo 0)
    thr=$(awk '/^Threads/{print $2}' /proc/$APPPID/status 2>/dev/null || echo 0)
  fi
  ma=$(awk '/^MemAvailable/{print $2}' /proc/meminfo)
  conn=$(ss -tnH state established 2>/dev/null | awk '{print $3}' | grep -c ':8000$')
  read -r ct ci <<<"$(awk '/^cpu /{idle=$5+$6; tot=0; for(i=2;i<=NF;i++) tot+=$i; print tot, idle}' /proc/stat)"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$(date +%s)" "${app_rss:-0}" "$ma" "$conn" "$ct" "$ci" "${app_j:-0}" "${thr:-0}" >> "$OUT"
  sleep 1
done
