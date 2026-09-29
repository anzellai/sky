#!/bin/bash
# Every 60 s for N minutes: RSS, fds, goroutines, heap stats of one PID.
S="$1"; PID="$2"; MIN="$3"; PP=http://127.0.0.1:6071/debug/pprof
echo "t_min rss_kb fds goroutines heap_alloc heap_inuse heap_sys heap_objects sys next_gc num_gc" > "$S/samples.tsv"
for m in $(seq 0 "$MIN"); do
  kill -0 "$PID" 2>/dev/null || { echo "relay gone at $m" >> "$S/samples.tsv"; exit 1; }
  rss=$(ps -o rss= -p "$PID" | tr -d ' ')
  fds=$(lsof -p "$PID" 2>/dev/null | tail -n +2 | wc -l | tr -d ' ')
  gor=$(curl -s --max-time 10 "$PP/goroutine?debug=1" | head -1 | awk '{print $NF}')
  h=$(curl -s --max-time 10 "$PP/heap?debug=1" | tail -40)
  f() { echo "$h" | awk -v k="# $1 =" 'index($0,k)==1 {print $4}'; }
  echo "$m $rss $fds $gor $(f HeapAlloc) $(f HeapInuse) $(f HeapSys) $(f HeapObjects) $(f Sys) $(f NextGC) $(f NumGC)" >> "$S/samples.tsv"
  if [ "$m" = 10 ] || [ "$m" = 40 ]; then
    curl -s --max-time 30 "$PP/heap?gc=1" -o "$S/heap-$m.pprof"
    curl -s --max-time 30 "$PP/goroutine?debug=1" -o "$S/goroutines-$m.txt"
  fi
  [ "$m" = "$MIN" ] && break
  sleep 60
done
