#!/usr/bin/env bash
# control.sh — the control arm and the erasure round trip, in ABSOLUTE objects
# per interaction rather than as a share of a total the change moves.
#
# A share is the wrong unit for a control. `rt.HtmlToVNode`'s share RISES when
# something else stops allocating, which reads as a regression in a pass the
# change cannot touch. So every number here is (self alloc_objects over the
# profile window) / (interactions in that window) — a quantity that should be
# invariant for a control frame and should fall for a targeted one.
set -euo pipefail
D="${1:?usage: control.sh <rundir>}"

# Interactions inside the profile window: throughput x the window's wall clock.
INTS=$(awk -v t="$(tr ',' '\n' < "$D/load.json" | awk -F: '/"interactions_per_sec"/{gsub(/[ ,]/,"",$2); print $2; exit}')" \
           -v w="$(awk '$1=="prof_wall_s"{print $2; exit}' "$D/profwindow.txt")" \
           'BEGIN{printf "%.0f", t*w}')

printf "%s\tprofile-window interactions %s\n" "$(basename "$D")" "$INTS"
go tool pprof -sample_index=alloc_objects -top -nodecount=100000 \
   -nodefraction=0 -edgefraction=0 \
   -base "$D/allocs-pre.pprof" "$D/allocs-post.pprof" 2>/dev/null |
awk -v ints="$INTS" '
  /^[ ]*[0-9]/ {
    # flat  flat%  sum%  cum  cum%  name…
    flat = $1
    mul = 1
    if (flat ~ /k$/) { mul = 1000;    sub(/k$/, "", flat) }
    if (flat ~ /M$/) { mul = 1000000; sub(/M$/, "", flat) }
    name = ""
    for (i = 6; i <= NF; i++) name = name (i > 6 ? " " : "") $i
    v = flat * mul
    if (name ~ /HtmlToVNode|renderVNode|applyStyleInjections|diffTrees|assignSkyIDs|reflect\.Value\.call$|rt\.asList$|rt\.AsListT|List_mapAny|List_filterMap$|List_filterAny|List_indexedMap$|List_mapT|List_filterT|List_filterMapT|List_indexedMapT|List_cons|skyCall|SkyCall/) {
      printf "  %-52s %10.1f objs/interaction\n", name, v / ints
    }
  }'
