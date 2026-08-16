#!/usr/bin/env bash
# summarise.sh — one TSV row per run directory. No jq, no python3 (neither is
# available here); awk only, same constraint the archived harness worked under.
#
# Columns, and how each is derived:
#   elements   viewsize.txt sky_id_elements -- counted from the HTML the app
#              SERVED during the run, never from an expectation
#   tput       load.json interactions_per_sec (measurement window only)
#   ints       load.json interactions_counted
#   patch_rate load.json patch_rate -- 1.0 or the run is not data
#   ms_win     prof_cpu_delta_s / (tput * prof_wall_s) * 1000
#              ps(1) CPU-time delta over the steady-state profile window,
#              divided by the interactions that fell in it. Independent of
#              pprof, so it doubles as the profiler-overhead cross-check.
#   ms_run     app_cpu_delta_s / ints * 1000 -- whole-run, includes ramp and
#              startup, so it reads high. Quoted only for comparability with
#              the archived scaling.tsv, which is computed this way.
#   objs_int   (loaded.mallocs - idle.mallocs) / ints
#   kb_int     (loaded.total_alloc - idle.total_alloc) / ints / 1024
set -euo pipefail

# jnum <file> <key> -- a top-level numeric/boolean JSON field. Handles BOTH
# shapes in this tree: skyliveload writes MarshalIndent (one field per line),
# the perf probe writes json.NewEncoder (the whole object on one line). Split
# on commas first so the two look the same.
jnum() {
  tr ',' '\n' < "$1" | awk -v k="\"$2\"" '
    { line = $0
      gsub(/^[ \t{]+/, "", line)
      if (index(line, k ":") == 1) {
        v = substr(line, length(k) + 2)
        gsub(/[ \t}]/, "", v)
        print v; exit
      }
    }'
}
kv() { awk -v k="$2" '$1==k {print $2; exit}' "$1"; }

printf "run\telements\ttput\tints\tpatch_rate\tms_win\tms_run\tobjs_int\tkb_int\tgen_cpu_pct\n"
for d in "$@"; do
  [ -f "$d/load.json" ] || { echo "$d	SKIPPED-no-load.json" >&2; continue; }
  if [ -f "$d/REJECTED" ]; then echo "$d	REJECTED: $(cat "$d/REJECTED")" >&2; continue; fi
  valid=$(jnum "$d/load.json" valid)
  [ "$valid" = "true" ] || { echo "$d	INVALID in load.json" >&2; continue; }

  el=$(kv "$d/viewsize.txt" sky_id_elements)
  tput=$(jnum "$d/load.json" interactions_per_sec)
  ints=$(jnum "$d/load.json" interactions_counted)
  pr=$(jnum "$d/load.json" patch_rate)
  gcpu=$(jnum "$d/load.json" generator_cpu_percent_of_machine)

  pcpu=$(kv "$d/profwindow.txt" prof_cpu_delta_s 2>/dev/null || echo "")
  pwall=$(kv "$d/profwindow.txt" prof_wall_s 2>/dev/null || echo "")
  acpu=$(kv "$d/cpu-accounting.txt" app_cpu_delta_s)

  m0=$(jnum "$d/memstats-idle.json" mallocs 2>/dev/null || echo "")
  m1=$(jnum "$d/memstats-loaded.json" mallocs 2>/dev/null || echo "")
  t0=$(jnum "$d/memstats-idle.json" total_alloc 2>/dev/null || echo "")
  t1=$(jnum "$d/memstats-loaded.json" total_alloc 2>/dev/null || echo "")

  awk -v run="$(basename "$(dirname "$d")")/$(basename "$d")" -v el="$el" -v tput="$tput" \
      -v ints="$ints" -v pr="$pr" -v pcpu="$pcpu" -v pwall="$pwall" -v acpu="$acpu" \
      -v m0="$m0" -v m1="$m1" -v t0="$t0" -v t1="$t1" -v gcpu="$gcpu" 'BEGIN{
    wints = (pwall != "" ? tput * pwall : 0)
    mswin = (wints > 0 && pcpu != "" ? pcpu / wints * 1000 : 0)
    msrun = (ints > 0 ? acpu / ints * 1000 : 0)
    objs  = (ints > 0 && m1 != "" && m0 != "" ? (m1 - m0) / ints : 0)
    kb    = (ints > 0 && t1 != "" && t0 != "" ? (t1 - t0) / ints / 1024 : 0)
    printf "%s\t%s\t%.1f\t%s\t%.3f\t%.3f\t%.3f\t%.0f\t%.1f\t%.2f\n",
      run, el, tput, ints, pr, mswin, msrun, objs, kb, gcpu
  }'
done
