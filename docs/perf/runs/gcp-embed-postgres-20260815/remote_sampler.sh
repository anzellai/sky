#!/bin/bash
# 1 Hz sampler: app RSS, postgres tree RSS, pg client backends, MemAvailable, conns.
# usage: remote_sampler.sh <seconds> <outfile>
SECS="${1:-120}"; OUT="${2:-/tmp/sample.tsv}"
SOCK=/tmp/sky-b5c107ad4fbf1900
PSQL=/usr/lib/postgresql/15/bin/psql
: > "$OUT"
for i in $(seq 1 "$SECS"); do
  APPPID=$(pgrep -f '/opt/skybench/app' | head -1)
  app_rss=0
  [ -n "$APPPID" ] && app_rss=$(awk '/^VmRSS/{print $2}' /proc/$APPPID/status 2>/dev/null || echo 0)
  pg_rss=0; pg_n=0
  for p in $(pgrep -x postgres 2>/dev/null); do
    r=$(awk '/^VmRSS/{print $2}' /proc/$p/status 2>/dev/null || echo 0)
    pg_rss=$((pg_rss + r)); pg_n=$((pg_n + 1))
  done
  # No socket-file guard: the socket dir is 0700 skybench, so a `test -S` run as
  # the SSH user fails even when the socket is there and reports 0 backends for
  # a cluster that has plenty. Ask postgres directly and let it fail if absent.
  be=$(sudo -u skybench $PSQL -h "$SOCK" -d postgres -Atc \
      "select count(*) from pg_stat_activity where backend_type='client backend';" 2>/dev/null || echo -1)
  bemax_all=$(sudo -u skybench $PSQL -h "$SOCK" -d postgres -Atc \
      "select count(*) from pg_stat_activity;" 2>/dev/null || echo -1)
  ma=$(awk '/^MemAvailable/{print $2}' /proc/meminfo)
  conn=$(ss -tn state established '( sport = :8000 )' 2>/dev/null | tail -n +2 | wc -l)
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$(date +%s)" "${app_rss:-0}" "$pg_rss" "$pg_n" "$be" "$ma" "$conn" "${bemax_all:--1}" >> "$OUT"
  sleep 1
done
