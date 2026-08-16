#!/usr/bin/env bash
# attrib.sh — the causal (overlapping) view of one run, plus the erased
# list-helper round trip's share of time and of allocation.
#
# The self-time buckets (bucket.sh) are disjoint and sum to 100%. These are
# CUMULATIVE and deliberately overlap -- `handleEvent` contains `view`,
# `view` contains `List_indexedMap`, `List_indexedMap` contains `SkyCall`.
# Both views are needed: the disjoint one says where the machine is, the
# cumulative one says which call chain put it there.
#
# Frames are matched, not summed, because summing cum% across nested frames
# double-counts. Each line stands alone.
set -euo pipefail
D="${1:?usage: attrib.sh <rundir>}"

FRAMES='handleEvent|liveApp\).dispatch|safeViewCall|Main_view|renderVNode|HtmlToVNode|diffTrees|applyStyleInjections|Std_Ui_layout$|Std_Ui_renderElement|Std_Ui_buildStyleString|Std_Ui_layoutContextFor|Std_Ui_hasMarker|View_Posts_postsListView|View_Posts_postRow|List_indexedMap|List_mapAny|List_filterMap|List_filterAny|List_cons|AsListT|asList$|rt\.SkyCall|skyCallDirect|skyCallOne|reflect\.Value\.Call|reflect\.Value\.call|reflect\.ValueOf|runtime\.mallocgc|runtime\.gcDrain|syscall\.write|bufio.*Flush|gcBgMarkWorker'

echo "### $D"
echo
echo "--- disjoint self-time buckets ---"
bash "$(dirname "$0")/bucket.sh" "$D/cpu.pprof"

echo
echo "--- cumulative (overlapping) CPU, selected frames ---"
go tool pprof -top -cum -nodecount=100000 -nodefraction=0 -edgefraction=0 "$D/cpu.pprof" 2>/dev/null |
  grep -E "flat%|$FRAMES" | head -45

if [ -f "$D/allocs-pre.pprof" ] && [ -f "$D/allocs-post.pprof" ]; then
  for idx in alloc_objects alloc_space; do
    echo
    echo "--- cumulative $idx over the profile window (allocs-post minus allocs-pre) ---"
    go tool pprof -sample_index="$idx" -top -cum -nodecount=100000 \
      -nodefraction=0 -edgefraction=0 \
      -base "$D/allocs-pre.pprof" "$D/allocs-post.pprof" 2>/dev/null |
      grep -E "^(File|Type|Time|Duration|Showing|Dropped)|flat%|$FRAMES" | head -45
  done
fi
