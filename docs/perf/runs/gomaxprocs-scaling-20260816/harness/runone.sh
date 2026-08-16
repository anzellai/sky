#!/usr/bin/env bash
# runone.sh — one (GOMAXPROCS, sessions, block) point of the scaling sweep.
# usage: runone.sh <gomaxprocs> <sessions> <block> [variant] [suffix]
set -u
S=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gmp
# The repo this file is committed in, not the worktree it was measured from: an
# archived harness has to read as wired wherever it is checked out, and this
# line named a sibling worktree that exists on exactly one machine. Gated by
# xtask's `every_lib_source_line_names_a_file_that_exists`.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
source "$REPO_ROOT/scripts/lib/with-timeout.sh"
GP=(--project settleby --zone us-central1-a)
APPIP=10.128.0.11

GMP="$1"; N="$2"; BLOCK="$3"; VARIANT="${4:-plain}"; SUF="${5:-}"
DUR="${DUR:-45}"; RAMP="${RAMP:-15}"; WARM="${WARM:-8}"
BIN=app; [ "$VARIANT" = prof ] && BIN=app-prof

TAG="g${GMP}-n${N}-b${BLOCK}${SUF}"
OUT="$S/out/$TAG"; mkdir -p "$OUT"
TSV="$S/out/results.tsv"
[ -f "$TSV" ] || printf 'tag\tgomaxprocs\tsessions\tblock\tvariant\telements\tthreads_idle\tidle_rss_kb\tload_rss_kb\tapp_cores\tbox_busy_pct\testablished\ttput\tp50\tp95\tp99\terr\tpatch_rate\tvalid\tgen_cpu_pct\tgen_sat\tgenbox_busy_pct\tinteractions\tgogc\twindow_rows\n' > "$TSV"

echo "=== $TAG ==="
IDLE=$(with_timeout 300 gcloud compute ssh skygmp-app "${GP[@]}" \
   --command "FORUM_POSTS=5 GOGC_SET=${GOGC_SET:-} MUTEX_FRACTION=${MUTEX_FRACTION:-5} BLOCK_RATE=${BLOCK_RATE:-10000} bash /tmp/remote_app.sh $GMP $VARIANT ${GCTRACE:-}" 2>/dev/null | grep -E '^(IDLE|FAIL)')
printf '%s\n' "$IDLE" >| "$OUT/idle.txt"
case "$IDLE" in IDLE*) echo "  $IDLE" ;;
  *) echo "  REJECTED app-setup: ${IDLE:-no output}"; printf '%s\n' "$IDLE" >| "$OUT/REJECTED"; exit 65 ;; esac
g() { sed -E "s/.*[ ]$1=([^ ]*).*/\1/" <<<"$IDLE"; }
ELS=$(g elements); IRSS=$(g app_rss_kb); THR=$(g threads); GOGCV=$(g gogc)

SECS=$(( RAMP + WARM + DUR + 45 ))
with_timeout 90 gcloud compute ssh skygmp-app "${GP[@]}" \
   --command "setsid bash /tmp/remote_sampler.sh $SECS /tmp/s.tsv $BIN </dev/null >/dev/null 2>&1 & echo started" >/dev/null 2>&1
sleep 3

RC=0
with_timeout 900 gcloud compute ssh skygmp-gen "${GP[@]}" \
   --command "bash /tmp/remote_gen.sh $APPIP $TAG $N $DUR $RAMP $WARM" >| "$OUT/run.txt" 2>&1 || RC=$?
if grep -q '^REJECT' "$OUT/run.txt"; then
  echo "  REJECTED self-check"; cp -f "$OUT/run.txt" "$OUT/REJECTED"; exit 66
fi
sed -n '/^{/,$p' "$OUT/run.txt" >| "$OUT/load.json"
GENBOX=$(grep -o 'GENBOX_BUSY_PCT=[0-9.]*' "$OUT/run.txt" | head -1 | cut -d= -f2)
GRC=$(grep -o 'GEN_RC=[0-9]*' "$OUT/run.txt" | head -1 | cut -d= -f2)

sleep 5
with_timeout 200 gcloud compute scp skygmp-app:/tmp/s.tsv "$OUT/sample.tsv" "${GP[@]}" >/dev/null 2>&1
with_timeout 200 gcloud compute scp skygmp-app:/tmp/app.log "$OUT/app.log" "${GP[@]}" >/dev/null 2>&1

J="$OUT/load.json"
jg() { grep -o "\"$1\": *[0-9.eE+-]*" "$J" 2>/dev/null | head -1 | sed 's/.*: *//'; }
jb() { grep -o "\"$1\": *[a-z]*" "$J" 2>/dev/null | head -1 | sed 's/.*: *//'; }
EST=$(jg sessions_established); TP=$(jg interactions_per_sec)
P50=$(jg p50_ms); P95=$(jg p95_ms); P99=$(jg p99_ms)
ER=$(jg error_rate); PR=$(jg patch_rate); GC=$(jg generator_cpu_percent_of_machine)
INTC=$(jg interactions_counted); VD=$(jb valid); GSAT=$(jb generator_possibly_saturated)

# Steady window, selected from the trace itself rather than from the clock.
#
# The sampler deliberately outlives the load by ~45 s so a slow teardown cannot
# truncate it, which means a `tail -N` window silently averages the app's IDLE
# tail into its CPU. The trial run read app_cores = 0.747 at GOMAXPROCS = 1 for
# exactly that reason while the raw jiffy slope over the load itself was 1.05.
# The window is therefore the rows where the app is actually holding the load's
# connections: conn8000 >= sessions (each session holds an SSE plus its POST
# connection), trimmed one row at each end.
SMP="$OUT/sample.tsv"
awk -F'\t' -v n="$N" '$4 >= n' "$SMP" 2>/dev/null | sed '1d;$d' >| "$OUT/window.tsv"
WROWS=$(wc -l < "$OUT/window.tsv" | tr -d ' ')
LRSS=$(awk -F'\t' '{print $2}' "$OUT/window.tsv" | sort -n | awk '{v[NR]=$1} END{if(NR)print v[int(NR/2)+1]; else print 0}')
read -r BOXB APPC <<<"$(awk -F'\t' '
  NR==1{t0=$5;i0=$6;a0=$7} {t1=$5;i1=$6;a1=$7}
  END{ dt=t1-t0; di=i1-i0;
       if(dt>0) printf "%.1f %.3f", 100*(dt-di)/dt, (a1-a0)/(dt/8.0); else printf "0 0" }' "$OUT/window.tsv")"
# app_cores = app jiffies / wall jiffies-per-core.  /proc/stat on this box was
# MEASURED at 800 jiffies/s against 8 x 100 owed, so this division is sound
# here in a way it was not on the shared-core instances of the capacity run.

printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
  "$TAG" "$GMP" "$N" "$BLOCK" "$VARIANT" "${ELS:-0}" "${THR:-0}" "${IRSS:-0}" "${LRSS:-0}" \
  "${APPC:-0}" "${BOXB:-0}" "${EST:-0}" "${TP:-0}" "${P50:-0}" "${P95:-0}" "${P99:-0}" \
  "${ER:-0}" "${PR:-0}" "${VD:-?}" "${GC:-0}" "${GSAT:-?}" "${GENBOX:-0}" "${INTC:-0}" "${GOGCV:-?}" \
  "${WROWS:-0}" >> "$TSV"

echo "  tput=$TP p50=$P50 err=$ER patch=$PR valid=$VD app_cores=$APPC box=$BOXB% gen=$GC% genbox=$GENBOX% ints=$INTC win=${WROWS}s"
[ "${GRC:-0}" -ne 0 ] && { echo "  GENERATOR RC=$GRC"; echo "generator exited $GRC" >| "$OUT/REJECTED"; }
exit 0
