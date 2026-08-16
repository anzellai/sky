#!/usr/bin/env bash
# bucket.sh — disjoint self-time decomposition of a Go CPU profile.
#
# The published showcase decomposition ("Go runtime and GC 42-46%, reflection
# machinery 11-12%, Sky runtime 3-4%, compiled Sky logic ~2%",
# docs/perf/skylive-interaction-cost.md:319-322) states four buckets summing to
# ~59% and NO classification rule. There is no script behind it. Comparing a
# second app against it therefore required writing the rule down; this is it,
# and it is applied identically to both profiles.
#
# Rules are ordered and FIRST MATCH WINS, which is what makes the buckets
# disjoint and the total exactly the profile's sample total.
#
#   1 write syscall       syscall. | internal/poll.
#   2 netpoll             runtime.{kevent,netpoll,epoll,usleep,semasleep,
#                                  pthread,notesleep}
#   3 memmove             runtime.{memmove,typedmemmove,bulkBarrierPreWrite,
#                                  memequal,typedslicecopy}
#   4 map/hash            internal/runtime/maps. | *hash* | internal/sync.HashTrieMap
#   5 reflect machinery   reflect. | internal/abi. | runtime.reflectcall |
#                         makeFuncAdapter | callRet
#   6 GC/allocator        every remaining runtime.
#   7 compiled Sky logic  main.        (the user's Sky, lowered to Go)
#   8 Sky runtime         sky-app/rt.  (runtime-go/rt)
#   9 scheduler/other     everything left (net/http, bufio, encoding/json, ...)
#
# -nodefraction=0 is REQUIRED. At pprof's default, ~400 nodes are dropped and
# the buckets sum to about 80% of samples, which would silently understate
# whichever bucket happens to be made of many small frames.
set -euo pipefail
PROF="${1:?usage: bucket.sh <cpu.pprof>}"

go tool pprof -top -nodecount=100000 -nodefraction=0 -edgefraction=0 "$PROF" 2>/dev/null |
awk '
function secs(v,   n,u) {
  n = v; sub(/[a-z]+$/, "", n) + 0
  u = v; sub(/^[0-9.]+/, "", u)
  if (u == "s")  return n + 0
  if (u == "ms") return n / 1000
  if (u == "us" || u == "µs") return n / 1000000
  if (u == "ns") return n / 1000000000
  return 0
}
/^ *flat/ { started = 1; next }
!started { next }
NF < 6 { next }
{
  flat = secs($1)
  sym = $6
  for (i = 7; i <= NF; i++) sym = sym " " $i
  total += flat

  if      (sym ~ /^syscall\.|^internal\/poll\./)                                    b = "1 write syscall"
  else if (sym ~ /^runtime\.(kevent|netpoll|epoll|usleep|semasleep|pthread|notesleep)/) b = "2 netpoll"
  else if (sym ~ /^runtime\.(memmove|typedmemmove|bulkBarrierPreWrite|memequal|typedslicecopy)/) b = "3 memmove"
  else if (sym ~ /^internal\/runtime\/maps\.|hash|^internal\/sync\.\(\*HashTrieMap/) b = "4 map+hash"
  else if (sym ~ /^reflect\.|^internal\/abi\.|^runtime\.reflectcall|makeFuncAdapter|^callRet/) b = "5 reflect machinery"
  else if (sym ~ /^runtime\./)                                                      b = "6 GC+allocator"
  else if (sym ~ /^main\./)                                                         b = "7 compiled Sky logic"
  else if (sym ~ /^sky-app\/rt\./)                                                  b = "8 Sky runtime"
  else                                                                              b = "9 scheduler+other"
  bucket[b] += flat
}
END {
  printf "%-24s %10s %8s\n", "bucket", "self_s", "pct"
  n = asorti(bucket, keys)
  for (i = 1; i <= n; i++)
    printf "%-24s %10.2f %7.1f%%\n", keys[i], bucket[keys[i]], 100*bucket[keys[i]]/total
  printf "%-24s %10.2f %7.1f%%\n", "TOTAL", total, 100
}'
