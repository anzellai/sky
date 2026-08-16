#!/usr/bin/env bash
# remote_prof.sh — grab mutex / block / cpu profiles from the instrumented app
# WHILE the load is running. Runs ON skygmp-app.
#
# The mutex and block profiles are cumulative from process start, so both are
# reset at the top of the measurement window and read again at the end: what is
# reported is contention DURING the load, not contention plus whatever the
# startup path did. Resetting is what makes the delta attributable.
#
# usage: remote_prof.sh <wait_before_s> <cpu_seconds> <tag>
set -u
WAIT="$1"; CPUSECS="$2"; TAG="$3"
D=/tmp/prof/$TAG; mkdir -p "$D"
P=http://127.0.0.1:6060/debug/pprof
sleep "$WAIT"
# Baseline reads, so the reported contention is the delta over the window.
curl -s -m 30 "$P/mutex" -o "$D/mutex.start.pprof"
curl -s -m 30 "$P/block" -o "$D/block.start.pprof"
curl -s -m $((CPUSECS + 30)) "$P/profile?seconds=$CPUSECS" -o "$D/cpu.pprof"
curl -s -m 30 "$P/mutex" -o "$D/mutex.end.pprof"
curl -s -m 30 "$P/block" -o "$D/block.end.pprof"
curl -s -m 30 "$P/goroutine?debug=1" -o "$D/goroutine.txt"
curl -s -m 30 "$P/heap" -o "$D/heap.pprof"
ls -la "$D"
echo PROF_DONE
