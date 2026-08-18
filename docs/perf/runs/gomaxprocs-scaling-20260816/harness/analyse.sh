#!/usr/bin/env bash
# analyse.sh — the scaling curve, from the recorded runs only.
#
# app_cores is RECOMPUTED here rather than taken from results.tsv. The driver's
# window admits any row holding at least `sessions` connections, which includes
# the tail of the ramp where establishment is still climbing; that dilutes the
# CPU slope. Here the window is narrowed to rows at the run's PLATEAU
# connection count, which is the only interval where the offered load is the
# full one.
set -u
S=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gmp
TSV="$S/out/results.tsv"

cores_at_plateau() {   # $1 = run dir
  local f="$1/sample.tsv"
  [ -f "$f" ] || { echo 0; return; }
  local mx
  mx=$(awk -F'\t' '{if($4>m)m=$4} END{print m+0}' "$f")
  awk -F'\t' -v mx="$mx" '$4>=mx' "$f" | sed '1d;$d' | awk -F'\t' '
    NR==1{t0=$5;a0=$7} {t1=$5;a1=$7}
    END{ dt=t1-t0; if(dt>0) printf "%.3f", (a1-a0)/(dt/8.0); else printf "0" }'
}

printf 'gomaxprocs\tblock\ttput\tapp_cores_plateau\tp50\tp95\terr\tgen_cpu_pct\tgenbox_pct\tvalid\n'
awk -F'\t' 'NR>1 && $4!=0 {print $1"\t"$2"\t"$4"\t"$13"\t"$14"\t"$15"\t"$17"\t"$20"\t"$22"\t"$19}' "$TSV" |
while IFS=$'\t' read -r tag gmp blk tp p50 p95 err gcp gbx vd; do
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$gmp" "$blk" "$tp" "$(cores_at_plateau "$S/out/$tag")" "$p50" "$p95" "$err" "$gcp" "$gbx" "$vd"
done | sort -n -k1,1 -k2,2

echo
echo "=== curve: median and range per level, speedup vs GOMAXPROCS=1 ==="
awk -F'\t' 'NR>1 && $4!=0 {print $2"\t"$13}' "$TSV" | sort -n | awk -F'\t' '
  {v[$1]=v[$1]" "$2}
  END{
    for(k in v){ n=split(v[k],a," "); asort_n(a,n); }
  }' 2>/dev/null
awk -F'\t' 'NR>1 && $4!=0 {print $2"\t"$13}' "$TSV" | sort -n -k1,1 -k2,2n | awk -F'\t' '
  { k=$1; c[k]++; val[k,c[k]]=$2; if(c[k]==1||$2<lo[k])lo[k]=$2; if($2>hi[k])hi[k]=$2 }
  END{
    printf "gomaxprocs\tn\tmedian\tmin\tmax\tspeedup_vs_1\tint_per_core\n"
    for(k in c){ m=val[k,int((c[k]+1)/2)]; med[k]=m }
    base=med[1]
    n=asorti(c, ks, "@ind_num_asc")
    for(i=1;i<=n;i++){ k=ks[i];
      printf "%s\t%d\t%.1f\t%.1f\t%.1f\t%.2f\t%.1f\n", k, c[k], med[k], lo[k], hi[k], med[k]/base, med[k]/k }
  }'
