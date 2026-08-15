#!/bin/bash
# Interleaved A/B/C sweep. Configs alternate WITHIN each level so e2 burst-credit
# state is shared across configs rather than confounding the comparison.
set -u
SD=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/pgbench
IP=34.59.219.179
GC="--project settleby --zone us-central1-a"
HOLD=60; RAMP=20; WARM=5
OUT="$SD/sweep.tsv"
mkdir -p "$SD/json" "$SD/samples"
[ -f "$OUT" ] || printf 'cfg\tlevel\trepeat\tidle_app_kb\tidle_pg_rss_kb\tidle_pg_pss_kb\tidle_app_pss_kb\tidle_pg_nproc\tidle_mem_avail_kb\tload_app_kb\tload_pg_rss_kb\tdelta_app_kb\tdelta_pg_kb\tpg_backends_max\testablished\tkb_per_session\ttput\tp50\tp95\tp99\terr\tvalid\tgen_cpu_pct\tstore\n' > "$OUT"

run_one() {
  local cfg="$1" n="$2" r="$3"
  local tag="${cfg}-n${n}-r${r}"
  echo "=== $tag ==="
  local idle
  idle=$(timeout 240 gcloud compute ssh sky-bench-embed $GC --command "bash /tmp/remote_setup.sh $cfg" 2>/dev/null | grep '^IDLE')
  if [ -z "$idle" ]; then echo "  SETUP FAILED"; return 1; fi
  echo "  $idle"
  local ia=$(sed -E 's/.*app_rss_kb=([0-9]+).*/\1/' <<<"$idle")
  local ip_=$(sed -E 's/.*app_pss_kb=([0-9]+).*/\1/' <<<"$idle")
  local gr=$(sed -E 's/.*pg_rss_kb=([0-9]+).*/\1/' <<<"$idle")
  local gp=$(sed -E 's/.*pg_pss_kb=([0-9]+).*/\1/' <<<"$idle")
  local gn=$(sed -E 's/.*pg_nproc=([0-9]+).*/\1/' <<<"$idle")
  local ma=$(sed -E 's/.*mem_avail_kb=([0-9]+).*/\1/' <<<"$idle")
  local st=$(sed -E 's/.*store=\[(.*)\]$/\1/' <<<"$idle" | tr ' ' '_')

  local secs=$((RAMP + HOLD + 35))
  timeout 60 gcloud compute ssh sky-bench-embed $GC --command \
    "setsid bash /tmp/remote_sampler.sh $secs /tmp/s.tsv < /dev/null > /dev/null 2>&1 & echo started" >/dev/null 2>&1
  sleep 3
  local J="$SD/json/$tag.json"
  timeout 400 "$SD/skyliveload" -remote-load -assume-yes -url "http://$IP:8000" \
     -sessions "$n" -duration "${HOLD}s" -ramp "${RAMP}s" -warmup "${WARM}s" \
     -think 1s -max-error-rate 1.0 -json "$J" -label "$tag" > "$SD/json/$tag.log" 2>&1
  sleep 8
  timeout 120 gcloud compute scp sky-bench-embed:/tmp/s.tsv "$SD/samples/$tag.tsv" $GC >/dev/null 2>&1

  local est tp p50 p95 p99 er vd gcpu
  est=$(grep -o '"sessions_established": *[0-9]*' "$J" | grep -o '[0-9]*$')
  tp=$(grep -o '"interactions_per_sec": *[0-9.]*' "$J" | grep -o '[0-9.]*$')
  p50=$(grep -o '"p50_ms": *[0-9.]*' "$J" | grep -o '[0-9.]*$')
  p95=$(grep -o '"p95_ms": *[0-9.]*' "$J" | grep -o '[0-9.]*$')
  p99=$(grep -o '"p99_ms": *[0-9.]*' "$J" | grep -o '[0-9.]*$')
  er=$(grep -o '"error_rate": *[0-9.]*' "$J" | grep -o '[0-9.]*$')
  vd=$(grep -o '"valid": *[a-z]*' "$J" | grep -o '[a-z]*$')
  gcpu=$(grep -o '"generator_cpu_percent_of_machine": *[0-9.]*' "$J" | grep -o '[0-9.]*$')

  # load RSS = median of the last 40 samples; pg backends = max over the window
  local la lg bem
  la=$(awk -F'\t' '{a[NR]=$2} END{n=asort(a); print a[int(n*0.5)]}' "$SD/samples/$tag.tsv" 2>/dev/null)
  la=$(tail -40 "$SD/samples/$tag.tsv" 2>/dev/null | awk -F'\t' '{print $2}' | sort -n | awk '{v[NR]=$1} END{print v[int(NR/2)+1]}')
  lg=$(tail -40 "$SD/samples/$tag.tsv" 2>/dev/null | awk -F'\t' '{print $3}' | sort -n | awk '{v[NR]=$1} END{print v[int(NR/2)+1]}')
  bem=$(awk -F'\t' '$5>m{m=$5} END{print m+0}' "$SD/samples/$tag.tsv" 2>/dev/null)

  local da=$(( ${la:-0} - ${ia:-0} )); local dg=$(( ${lg:-0} - ${gr:-0} ))
  local kbs="NA"
  [ "${est:-0}" -gt 0 ] 2>/dev/null && kbs=$(awk -v d="$da" -v e="$est" 'BEGIN{printf "%.0f", d/e}')
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$cfg" "$n" "$r" "$ia" "$gr" "$gp" "$ip_" "$gn" "$ma" "${la:-0}" "${lg:-0}" "$da" "$dg" "${bem:-0}" \
    "${est:-0}" "$kbs" "${tp:-0}" "${p50:-0}" "${p95:-0}" "${p99:-0}" "${er:-0}" "${vd:-?}" "${gcpu:-0}" "$st" >> "$OUT"
  echo "  est=$est tput=$tp p50=$p50 backends_max=$bem kb/sess=$kbs valid=$vd"
}

for spec in "$@"; do
  IFS=: read -r n reps <<< "$spec"
  for r in $(seq 1 "$reps"); do
    for cfg in A B C; do
      run_one "$cfg" "$n" "$r"
    done
  done
done
echo "SWEEP COMPLETE"
