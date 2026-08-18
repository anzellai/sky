#!/usr/bin/env bash
# remote_sampler.sh — 1 Hz trace on a TARGET while the load runs.
# usage: remote_sampler.sh <seconds> <outfile>
#
# Columns:
#   epoch app_rss_kb pg_tree_rss_kb pg_nproc client_backends mem_avail_kb
#   conn8000 cpu_total_jiffies cpu_idle_jiffies app_jiffies pg_jiffies
#   xact_commit blks_read wal_records
#
# The CPU jiffy columns are what decide whether the box is CPU-bound: a
# saturated 0.5-vCPU e2-small shows app_jiffies climbing at the cgroup
# ceiling while throughput sits flat. The pg_stat columns are what decide
# whether PostgreSQL is the binder rather than a passenger.
SECS="${1:-120}"; OUT="${2:-/tmp/sample.tsv}"
PGBIN=/usr/lib/postgresql/15/bin
SOCK=$(ls -d /tmp/sky-* 2>/dev/null | head -1)
: > "$OUT"
# `pgrep -x app`, not `pgrep -f /opt/skybench/app | head -1`: the latter also
# matches the `sudo -u skybench env … /opt/skybench/app` wrapper, whose pid is
# LOWER, so head -1 returns the wrapper. Measured on this box: wrapper rss
# 11956 kB and 1 jiffy for the whole run, app rss 23400 kB. Re-resolved every
# iteration so a restart mid-sample shows up as a pid change rather than as a
# frozen row.
for i in $(seq 1 "$SECS"); do
  APPPID=$(pgrep -x app | head -1)
  app_rss=0; app_j=0
  if [ -n "$APPPID" ] && [ -d "/proc/$APPPID" ]; then
    app_rss=$(awk '/^VmRSS/{print $2}' /proc/$APPPID/status 2>/dev/null || echo 0)
    app_j=$(awk '{print $14+$15}' /proc/$APPPID/stat 2>/dev/null || echo 0)
  fi
  pg_rss=0; pg_n=0; pg_j=0
  for p in $(pgrep -x postgres 2>/dev/null); do
    r=$(awk '/^VmRSS/{print $2}' /proc/$p/status 2>/dev/null || echo 0)
    j=$(awk '{print $14+$15}' /proc/$p/stat 2>/dev/null || echo 0)
    pg_rss=$((pg_rss + r)); pg_j=$((pg_j + j)); pg_n=$((pg_n + 1))
  done
  be=-1; xc=-1; wr=-1
  if [ -n "$SOCK" ]; then
    read -r be xc wr <<<"$(sudo -u skybench $PGBIN/psql -h "$SOCK" -d postgres -Atc \
      "select (select count(*) from pg_stat_activity where backend_type='client backend'),
              (select sum(xact_commit) from pg_stat_database),
              (select wal_records from pg_stat_wal);" 2>/dev/null | tr '|' ' ')"
  fi
  ma=$(awk '/^MemAvailable/{print $2}' /proc/meminfo)
  # Counted by matching the local address column rather than with ss's own
  # `sport =` filter expression, which returned 0 against connections that
  # were demonstrably open when this harness was validated.
  conn=$(ss -tnH state established 2>/dev/null | awk '{print $3}' | grep -c ':8000$')
  read -r ct ci <<<"$(awk '/^cpu /{idle=$5+$6; tot=0; for(i=2;i<=NF;i++) tot+=$i; print tot, idle}' /proc/stat)"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$(date +%s)" "${app_rss:-0}" "$pg_rss" "$pg_n" "${be:--1}" "$ma" "$conn" \
    "$ct" "$ci" "${app_j:-0}" "$pg_j" "${xc:--1}" "${wr:--1}" >> "$OUT"
  sleep 1
done
