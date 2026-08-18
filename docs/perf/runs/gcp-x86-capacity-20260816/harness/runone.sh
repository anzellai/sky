#!/usr/bin/env bash
# runone.sh — one (target, config, n, repeat) point.
#
# usage: runone.sh <small|medium> <mem|pg|pgnofsync> <n> <rep> <block> [posts]
#
# The generator lives on its own box in the same zone, so the load path is
# an internal ~0.2 ms hop rather than the ~111 ms UK->us-central1 RTT that
# every earlier remote run in this corpus carried in its latency columns.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench
# The repo this file is committed in, not the worktree it was measured from: an
# archived harness has to read as wired wherever it is checked out, and this
# line named a sibling worktree that exists on exactly one machine. Gated by
# xtask's `every_lib_source_line_names_a_file_that_exists`.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
source "$REPO_ROOT/scripts/lib/with-timeout.sh"

TGT="$1"; CFG="$2"; N="$3"; REP="$4"; BLOCK="$5"; POSTS="${6:-5}"
HOLD="${HOLD:-90}"; RAMP="${RAMP:-25}"; WARM="${WARM:-10}"; THINK="${THINK:-0}"

INST="skyperf-$TGT"
IPFILE="$BASE/ips.txt"
TIP=$(awk -v n="$INST" '$1==n{print $2}' "$IPFILE")
[ -n "$TIP" ] || { echo "no internal IP for $INST in $IPFILE" >&2; exit 2; }

TAG="${TGT}-${CFG}-n${N}-b${BLOCK}r${REP}"
OUT="$BASE/out/$TAG"
mkdir -p "$OUT"
TSV="$BASE/out/results.tsv"
[ -f "$TSV" ] || printf 'target\tcfg\tposts\tblock\trep\tlevel\telements\tstore\tidle_app_kb\tidle_pg_pss_kb\tidle_mem_avail_kb\tfsync\tshared_buffers\tmax_connections\tload_app_kb\tload_pg_rss_kb\tbackends_max\tcpu_busy_pct\tapp_cpu_pct\tpg_cpu_pct\testablished\ttput\tp50\tp95\tp99\terr\tpatch_rate\tvalid\tgen_cpu_pct\txact_per_s\twal_rec_per_s\tgen_saturated\tinteractions\n' > "$TSV"

echo "=== $TAG ==="

# ---- 1. bring the app up in this configuration, store ASSERTED -----------
IDLE=$(with_timeout 300 gcloud compute ssh "$INST" --project settleby --zone us-central1-a \
   --command "FORUM_POSTS=$POSTS bash /tmp/remote_app.sh $CFG" 2>/dev/null | grep -E '^(IDLE|FAIL)')
echo "$IDLE" >| "$OUT/idle.txt"
case "$IDLE" in
  IDLE*) ;;
  *) echo "  SETUP FAILED: $IDLE"; echo "$IDLE" >| "$OUT/REJECTED"; exit 65;;
esac
echo "  $IDLE"
g() { sed -E "s/.*[ ]$1=([^ ]*).*/\1/" <<<"$IDLE"; }
ST=$(g store); IA=$(g app_rss_kb); IP_=$(g pg_pss_kb); MA=$(g mem_avail_kb)
ELS=$(g elements); FS=$(g fsync); SB=$(g shared_buffers); MC=$(g max_connections)

# ---- 2. PRECONDITION: this handler patches on every press -----------------
if ! with_timeout 180 gcloud compute ssh skyperf-gen --project settleby --zone us-central1-a \
   --command "ulimit -n 65535; /opt/gen/skyliveload -url http://$TIP:8000 -remote-load -assume-yes -self-check -setup /opt/gen/forum-setup.json -hid-suffix .click -hid-context '>▲<'" \
   >| "$OUT/selfcheck.txt" 2>&1; then
  echo "  SELF-CHECK FAILED"; tail -20 "$OUT/selfcheck.txt"
  echo "self-check failed" >| "$OUT/REJECTED"; exit 66
fi

# ---- 3. sampler on the target, then the load from the generator ----------
SECS=$(( RAMP + HOLD + 40 ))
with_timeout 90 gcloud compute ssh "$INST" --project settleby --zone us-central1-a \
   --command "setsid bash /tmp/remote_sampler.sh $SECS /tmp/s.tsv </dev/null >/dev/null 2>&1 & echo started" >/dev/null 2>&1
sleep 3

GEN_RC=0
with_timeout 900 gcloud compute ssh skyperf-gen --project settleby --zone us-central1-a \
  --command "ulimit -n 65535; /opt/gen/skyliveload -url http://$TIP:8000 -remote-load -assume-yes \
     -sessions $N -duration ${HOLD}s -ramp ${RAMP}s -warmup ${WARM}s -think $THINK \
     -max-error-rate 1.0 -min-patch-rate 0.9 \
     -setup /opt/gen/forum-setup.json -hid-suffix .click -hid-context '>▲<' \
     -json /tmp/load.json -label $TAG; echo GEN_RC=\$?; cat /tmp/load.json" \
  >| "$OUT/load.txt" 2>&1 || GEN_RC=$?

sed -n '/^{/,$p' "$OUT/load.txt" >| "$OUT/load.json"
RC_LINE=$(grep -o 'GEN_RC=[0-9]*' "$OUT/load.txt" | head -1 | cut -d= -f2)
[ -n "$RC_LINE" ] && GEN_RC="$RC_LINE"

sleep 6
with_timeout 180 gcloud compute scp "$INST":/tmp/s.tsv "$OUT/sample.tsv" \
   --project settleby --zone us-central1-a >/dev/null 2>&1

J="$OUT/load.json"
jg() { grep -o "\"$1\": *[0-9.eE+-]*" "$J" 2>/dev/null | head -1 | sed 's/.*: *//'; }
EST=$(jg sessions_established); TP=$(jg interactions_per_sec)
P50=$(jg p50_ms); P95=$(jg p95_ms); P99=$(jg p99_ms)
ER=$(jg error_rate); PR=$(jg patch_rate); GC=$(jg generator_cpu_percent_of_machine)
VD=$(grep -o '"valid": *[a-z]*' "$J" 2>/dev/null | head -1 | sed 's/.*: *//')
GSAT=$(grep -o '"generator_possibly_saturated": *[a-z]*' "$J" 2>/dev/null | head -1 | sed 's/.*: *//')
INTC=$(jg interactions_counted)

# steady-state window = last 40 samples, i.e. after ramp and after the burst
# credits of THIS run are already being spent
S="$OUT/sample.tsv"
med() { tail -40 "$S" 2>/dev/null | awk -F'\t' -v c="$1" '{print $c}' | sort -n | awk '{v[NR]=$1} END{if(NR)print v[int(NR/2)+1]; else print 0}'; }
LA=$(med 2); LG=$(med 3)
BEM=$(awk -F'\t' '$5>m{m=$5} END{print m+0}' "$S" 2>/dev/null)
# CPU: jiffy deltas across the steady window
read -r CB AP PG XPS WPS <<<"$(tail -40 "$S" 2>/dev/null | awk -F'\t' '
  NR==1{t0=$8;i0=$9;a0=$10;p0=$11;x0=$12;w0=$13;s0=$1}
  {t1=$8;i1=$9;a1=$10;p1=$11;x1=$12;w1=$13;s1=$1}
  END{ dt=t1-t0; di=i1-i0; ds=s1-s0;
       if(dt>0){ printf "%.1f %.1f %.1f ", 100*(dt-di)/dt, 100*(a1-a0)/dt, 100*(p1-p0)/dt }
       else printf "0 0 0 ";
       if(ds>0 && x0>0) printf "%.1f %.1f", (x1-x0)/ds, (w1-w0)/ds; else printf "NA NA" }')"

printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
  "$TGT" "$CFG" "$POSTS" "$BLOCK" "$REP" "$N" "${ELS:-0}" "${ST:-?}" "${IA:-0}" "${IP_:-0}" "${MA:-0}" \
  "${FS:-NA}" "${SB:-NA}" "${MC:-NA}" "${LA:-0}" "${LG:-0}" "${BEM:-0}" \
  "${CB:-0}" "${AP:-0}" "${PG:-0}" \
  "${EST:-0}" "${TP:-0}" "${P50:-0}" "${P95:-0}" "${P99:-0}" "${ER:-0}" "${PR:-0}" "${VD:-?}" "${GC:-0}" \
  "${XPS:-NA}" "${WPS:-NA}" "${GSAT:-?}" "${INTC:-0}" >> "$TSV"

echo "  est=$EST tput=$TP p50=$P50 err=$ER patch_rate=$PR valid=$VD cpu_busy=$CB% app=$AP% pg=$PG% backends=$BEM"
if [ "${GEN_RC:-0}" -ne 0 ]; then
  echo "  GENERATOR RC=$GEN_RC — run flagged"
  echo "generator exited $GEN_RC" >| "$OUT/REJECTED"
fi
exit 0
