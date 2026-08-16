#!/usr/bin/env bash
# control.sh — Stage 3 attribution, in ABSOLUTE objects per interaction.
#
# Unchanged in method from Stage 2's: a SHARE is the wrong unit for a control,
# because an untouched pass's share RISES when something else stops allocating,
# which reads as a regression it did not have. Every number here is
# (self alloc_objects over the profile window) / (interactions in that window).
#
# THE CONTROL IS NOT THE ONE STAGE 1 AND STAGE 2 USED.
#
# `rt.HtmlToVNode` is contaminated. Measured on
# ../stage2-typed-hof-20260816/p60-after-r1: it is 52.48% of the callers of
# `rt.asList` (12,755,241 objects), and `asList` sends 67.96% of its cumulative
# into `reflect.Value.Interface` — the exact frame Stage 3's headline is defined
# on. A control cannot sit inside the mechanism under test. It is printed below
# as a WITNESS instead: it should barely move, and if it moves a lot the change
# reached further than the claim.
#
# The control is `rt.(*VNode).setAttr`: attribute-count-proportional, below the
# Sky boundary, a single caller (`rt.applyHtmlAttr`), and NO reflect subtree —
# its cum equals its flat, 4,976,073 objects = 1.31% of all allocation, which is
# adequate sampling. It was also the tightest frame across Stage 2, moving
# +1.7% / -2.1% on a change that could not touch it.
#
# SECOND CONTROL, and the only one that tests the ROUTING rather than the host:
# a deliberately-erased `List.foldl` call site in the bench app whose element
# type is `any`. Its `rt.AsListT[any]` must NOT fall. `setAttr` controls the
# machine; the negative control catches a predicate that fires everywhere.
#
# Do NOT quote a "spread between identical runs" from Stage 2's README: only r1
# carries allocs-*.pprof in each arm there, so its attribution is n = 1 and the
# 6.4%/6.6% figures cannot be recomputed. This run commits all three profiles
# per arm and reports the attribution as a RANGE.
set -euo pipefail
D="${1:?usage: control.sh <rundir>}"

INTS=$(awk -v t="$(tr ',' '\n' < "$D/load.json" | awk -F: '/"interactions_per_sec"/{gsub(/[ ,]/,"",$2); print $2; exit}')" \
           -v w="$(awk '$1=="prof_wall_s"{print $2; exit}' "$D/profwindow.txt")" \
           'BEGIN{printf "%.0f", t*w}')

printf "%s\tprofile-window interactions %s\n" "$(basename "$D")" "$INTS"
go tool pprof -sample_index=alloc_objects -top -nodecount=100000 \
   -nodefraction=0 -edgefraction=0 \
   -base "$D/allocs-pre.pprof" "$D/allocs-post.pprof" 2>/dev/null |
awk -v ints="$INTS" '
  /^[ ]*[0-9]/ {
    flat = $1
    mul = 1
    if (flat ~ /k$/) { mul = 1000;    sub(/k$/, "", flat) }
    if (flat ~ /M$/) { mul = 1000000; sub(/M$/, "", flat) }
    name = ""
    for (i = 6; i <= NF; i++) name = name (i > 6 ? " " : "") $i
    v = flat * mul
    # control | witness | targeted | mechanism
    if (name ~ /setAttr|applyHtmlAttr|HtmlToVNode|renderVNode|assignSkyIDs|reflect\.Value\.Interface|reflect\.Value\.call$|rt\.asList$|rt\.AsList$|rt\.AsListT|rt\.Coerce|rt\.List_foldlT|rt\.List_foldlElemFirstT|rt\.List_anyT|Sky_Core_List_foldl|Sky_Core_List_any_|Std_Ui_markerFlags|Std_Ui_hasMarker|Std_Ui_buildStyleStringWith|skyCall|SkyCall/) {
      printf "  %-60s %10.1f objs/interaction\n", name, v / ints
    }
  }'
