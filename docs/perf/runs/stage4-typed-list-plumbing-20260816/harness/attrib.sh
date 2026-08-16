#!/usr/bin/env bash
# attrib.sh — the attribution table: one row per frame, per-run columns for both
# arms, in ABSOLUTE objects per interaction. See control.sh's header for why a
# share is the wrong unit and why the controls are the ones they are.
#
#   usage: attrib.sh <size>       e.g. attrib.sh p5   /   attrib.sh p60
set -euo pipefail
S="${S:-/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/stage4}"
SZ="${1:?usage: attrib.sh p5|p60}"
REPS="${REPS:-3}"

# Frame -> exact name emitted by pprof -top. An absent frame prints `-`, which is
# meaningful: `rt.Concat` going absent in the after arm IS the result.
FRAMES=(
  "reflect.unsafe_New"
  "sky-app/rt.Concat"
  "sky-app/rt.AsList"
  "sky-app/rt.AsListT[go.shape.struct { Tag int; SkyName string; Fields []interface {} }]"
  "sky-app/rt.List_appendT[go.shape.struct { Tag int; SkyName string; Fields []interface {} }]"
  "sky-app/rt.List_cons"
  "sky-app/rt.(*VNode).setAttr"
  "sky-app/rt.HtmlToVNode"
  "main.Std_Ui_button.func1"
  "main.Std_Ui_renderNodeAs.func1"
)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for arm in before after; do
  for r in $(seq 1 "$REPS"); do
    d="$S/runs/$SZ-$arm-r$r"
    [ -d "$d" ] || continue
    bash "$S/harness/control.sh" "$d" 2>/dev/null > "$tmp/$arm-$r"
  done
done

printf "%-58s %26s   %26s\n" "frame (self objs/interaction)" "before r1/r2/r3" "after r1/r2/r3"
for f in "${FRAMES[@]}"; do
  short=$(printf '%s' "$f" | sed 's/\[go.shape.struct { Tag int; SkyName string; Fields \[\]interface {} }\]/[SkyADT]/; s/ (inline)//')
  printf "%-58s" "$short"
  for arm in before after; do
    for r in $(seq 1 "$REPS"); do
      [ -f "$tmp/$arm-$r" ] || continue
      # Exact frame match on the name column, which control.sh left-pads by 2.
      # pprof suffixes a frame with ` (inline)` or ` (partial-inline)` depending
      # on how the caller inlined it, and that suffix DIFFERS between the two
      # arms for the same frame. Strip both before comparing, or the after arm
      # silently reads `-` and a real number is reported as an absence.
      v=$(awk -v want="$f" '{
             line=$0
             sub(/^  /, "", line)
             sub(/ +[0-9.]+ objs\/interaction$/, "", line)
             sub(/ \(partial-inline\)$/, "", line)
             sub(/ \(inline\)$/, "", line)
             sub(/ +$/, "", line)
             if (line == want) { print $(NF-1); exit }
           }' "$tmp/$arm-$r")
      printf " %8s" "${v:--}"
    done
    [ "$arm" = before ] && printf "  |"
  done
  echo
done
