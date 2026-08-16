#!/usr/bin/env bash
# summarise.sh — one TSV row per run.
#
# THE PROFILE-WINDOW DENOMINATOR IS `prof_cpu_delta_s`, NOT `prof_wall_s`.
#
# Stage 2 and Stage 3 derived objects-per-interaction as
# (window objects) / (throughput * prof_wall_s), and `prof_wall_s` is
# `$(date +%s)` differenced — integer seconds. On a 25 s window it lands on 25 or
# 26 depending only on where the two calls fell inside their seconds, and it did
# both WITHIN this run set (p5-before-r1/r2 read 26, every other p5 run read 25).
# That is a 4% swing in the denominator, applied per-run, to a quantity whose
# real effect here is 15% — enough to manufacture an outlier out of nothing, and
# `p5-before-r3` looked like one until this was checked.
#
# `prof_cpu_delta_s` is the app's own CPU time across the same window, read to
# 0.01 s, and with GOMAXPROCS=1 against a saturating closed-loop generator it is
# the honest measure of how much app work the window covers. It reads 25.17-25.21
# across every run in this set, which is the cross-check that the two arms were
# profiled over equal work rather than equal wall clock.
#
# Both denominators are printed so the artefact stays visible rather than being
# silently corrected away.
set -euo pipefail
S="${S:-/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/stage4}"
printf "run\telements\ttput\tcpu_s\twall_s\tints\tpatch_rate\tobjs_int\tkb_int\tvalid\n"
for d in "$S"/runs/p*/; do
  n=$(basename "$d")
  [ -f "$d/load.json" ] || continue
  [ -f "$d/allocs-post.pprof" ] || continue
  el=$(awk '$1=="sky_id_elements"{print $2; exit}' "$d/viewsize.txt" 2>/dev/null || echo "?")
  t=$(tr ',' '\n' < "$d/load.json" | awk -F: '/"interactions_per_sec"/{gsub(/[ ,]/,"",$2); print $2; exit}')
  pr=$(tr ',' '\n' < "$d/load.json" | awk -F: '/"patch_rate"/{gsub(/[ ,]/,"",$2); print $2; exit}')
  vd=$(tr ',' '\n' < "$d/load.json" | awk -F: '/"valid"/{gsub(/[ ,]/,"",$2); print $2; exit}')
  cpu=$(awk '$1=="prof_cpu_delta_s"{print $2; exit}' "$d/profwindow.txt")
  wall=$(awk '$1=="prof_wall_s"{print $2; exit}' "$d/profwindow.txt")
  ints=$(awk -v t="$t" -v w="$cpu" 'BEGIN{printf "%.0f", t*w}')
  # The total is field 8 of `Showing nodes accounting for X, P% of TOTAL total`.
  objs=$(go tool pprof -sample_index=alloc_objects -base "$d/allocs-pre.pprof" -top -nodecount=1 "$d/allocs-post.pprof" 2>/dev/null |
         awk -v i="$ints" '/^Showing nodes accounting for/{v=$8; gsub(/,/,"",v); if(v~/k$/){sub(/k$/,"",v);v*=1000}; if(v~/M$/){sub(/M$/,"",v);v*=1000000}; if(v~/G$/){sub(/G$/,"",v);v*=1000000000}; printf "%.0f", v/i; exit}')
  kb=$(go tool pprof -sample_index=alloc_space -base "$d/allocs-pre.pprof" -top -nodecount=1 "$d/allocs-post.pprof" 2>/dev/null |
       awk -v i="$ints" '/^Showing nodes accounting for/{v=$8; if(v~/kB$/){sub(/kB$/,"",v)}else if(v~/MB$/){sub(/MB$/,"",v);v*=1024}else if(v~/GB$/){sub(/GB$/,"",v);v*=1048576}else if(v~/TB$/){sub(/TB$/,"",v);v*=1073741824}; printf "%.1f", v/i; exit}')
  printf "%s\t%s\t%.1f\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n" "$n" "$el" "$t" "$cpu" "$wall" "$ints" "$pr" "$objs" "$kb" "$vd"
done
