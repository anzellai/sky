#!/usr/bin/env bash
# control.sh — Stage 4 attribution, in ABSOLUTE objects per interaction.
#
# Unchanged in method from Stage 2's and Stage 3's: a SHARE is the wrong unit
# for a control, because an untouched pass's share RISES when something else
# stops allocating, which reads as a regression it did not have. Every number
# here is (self alloc_objects over the profile window) / (interactions in that
# window).
#
# THE NEGATIVE CONTROL IS `rt.List_cons`, AND STAGE 3'S CHOICE WAS UNAVAILABLE.
#
# Stage 3's strongest line was a DIFFERENT INSTANTIATION of the helper it
# targeted: `rt.AsListT[rt.SkyADT]` had to hold still while `rt.AsListT[
# interface{}]` fell 98.9%, which separates "the routing predicate fired where
# it should" from "the predicate fired everywhere". That control cannot be
# reproduced here. Every list this change touches in `Std_Ui.renderNodeAs` has
# element type `Std_Ui_Attribute` / `Std_Html_Attributes_Attribute` /
# `Std_Html_Html` / `Std_Ui_Element`, and ALL FOUR are `= rt.SkyADT` in the
# emitted Go (main.go:187, :211, :227, :339). They are one Go type, so they are
# one stencil, and there is no sibling instantiation left to hold still.
#
# `rt.List_cons` replaces it, and is the better control for this particular
# change anyway. `::` is `++`'s structural sibling: it emits the same
# `rt.X(any(a), any(b))` widen pair, returns the same `any`, is re-narrowed by
# the same `rt.AsListT[T]`, and runs in the SAME function at the SAME per-element
# frequency (`attrList_24` in `renderNodeAs`). It is deliberately NOT re-pointed
# by this change. If the predicate had fired on operator shape rather than on
# proven operand types, `List_cons` would have fallen with `Concat`; it must not.
#
# SECOND NEGATIVE CONTROL, and it appeared rather than being constructed: the
# ONE `rt.Concat` that survives in the after arm (`main.Std_Ui_button.func1`,
# main.go:707). Its right operand is an `rt.List_cons(...)` typed `any`, so
# `provable()` refuses it — the same refusal class Stage 3 reported. Its caller
# edge must still be there afterwards. A change that zeroed `rt.Concat` outright
# would mean the predicate stopped checking its operands.
#
# HOST CONTROL: `rt.(*VNode).setAttr` — Stage 3's, kept for continuity.
# Attribute-count-proportional, below the Sky boundary, one caller, no reflect
# subtree. WITNESS: `rt.HtmlToVNode`, which is contaminated (it is 51-54% of the
# callers of `rt.asList`) and is reported as a witness, never as a control.
set -euo pipefail
D="${1:?usage: control.sh <rundir>}"

# The denominator is `prof_cpu_delta_s`, NOT `prof_wall_s` — see summarise.sh's
# header. `prof_wall_s` is integer-second `date +%s` arithmetic and lands on 25
# or 26 for the same 25 s window, a 4% per-run swing that this run set actually
# exhibited.
INTS=$(awk -v t="$(tr ',' '\n' < "$D/load.json" | awk -F: '/"interactions_per_sec"/{gsub(/[ ,]/,"",$2); print $2; exit}')" \
           -v w="$(awk '$1=="prof_cpu_delta_s"{print $2; exit}' "$D/profwindow.txt")" \
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
    # targeted | replacement | negative control | host control | witness
    if (name ~ /rt\.Concat$|rt\.AsList$|rt\.AsListT|rt\.List_appendT|rt\.List_cons$|setAttr|HtmlToVNode|reflect\.unsafe_New|reflect\.packEfaceData|reflect\.Value\.Interface|renderNodeAs|Std_Ui_button/) {
      printf "  %-72s %10.1f objs/interaction\n", name, v / ints
    }
  }'
