#!/usr/bin/env bash
# analyse.sh — turn each repeat's raw artefacts into the reported row.
#
# CPU-ms per interaction is computed over EXACTLY the generator's counted
# window, not over the process lifetime: the generator discards a 3 s warmup,
# so lifetime CPU divided by counted interactions would charge the warmup's
# work to the measured ones. The window is
#   [started_at + warmup, started_at + warmup + duration]
# and `started_at` is RFC3339 to the second, which is why the sampler is kept
# alive past the generator's exit — see remote_run.sh.
set -u
CORE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench/core
CLK=100; WARM=3; DUR=45

printf 'rep\tips\tp50_ms\tp95_ms\tp99_ms\tcpu_s_win\tcpu_ms_per_int\tapp_core_pct\trss_idle_kb\trss_load_med_kb\trss_load_max_kb\tints\terr\tpatch_rate\tvalid\tgen_cpu_pct\tgen_sat\tels\tcover\n'

for r in 1 2 3; do
  D="$CORE/core-mem-p5-n25-r$r"
  J="$D/load.json"
  [ -f "$J" ] || { echo "r$r MISSING"; continue; }
  jg() { grep -o "\"$1\": *[0-9.eE+-]*" "$J" | head -1 | sed 's/.*: *//'; }
  jb() { grep -o "\"$1\": *[a-z]*" "$J" | head -1 | sed 's/.*: *//'; }
  IPS=$(jg interactions_per_sec); P50=$(jg p50_ms); P95=$(jg p95_ms); P99=$(jg p99_ms)
  ER=$(jg error_rate); PR=$(jg patch_rate); VD=$(jb valid)
  INT=$(jg interactions_counted); GC=$(jg generator_cpu_percent_of_machine); GS=$(jb generator_possibly_saturated)
  SA=$(grep -oE '"started_at": *"[^"]*"' "$J" | sed 's/.*"\(2[^"]*\)"/\1/')
  T0=$(date -u -d "$SA" +%s); W0=$((T0+WARM)); W1=$((W0+DUR))
  ELS=$(sed -E 's/.* elements=([0-9]+).*/\1/' "$D/idle.txt")
  RSSI=$(sed -E 's/.* app_rss_kb=([0-9]+).*/\1/' "$D/idle.txt")

  read -r CPUS CPUMS PCT RMED RMAX COVER <<<"$(awk -F'\t' -v w0="$W0" -v w1="$W1" -v clk="$CLK" -v ints="$INT" '
    { t=$1+0
      if (t>=w0 && t<=w1) {
        if (j0=="") { j0=$3; t0=t }
        j1=$3; t1=t; rs[++n]=$2
      }
      lo=(lo==""?t:lo); hi=t }
    END{
      if (n<2) { print "NA NA NA NA NA no-coverage"; exit }
      dj=j1-j0; dt=t1-t0
      cpus=dj/clk
      # Scale the jiffy delta to the FULL window: the sampler ticks at 250 ms so
      # the first/last in-window samples sit slightly inside the edges.
      if (dt>0) cpus_full=cpus*( (w1-w0)/dt ); else cpus_full=cpus
      asort_n=n; for(i=1;i<=n;i++) v[i]=rs[i]
      for(i=1;i<n;i++) for(k=i+1;k<=n;k++) if(v[k]<v[i]){x=v[i];v[i]=v[k];v[k]=x}
      med=v[int(n/2)+1]; mx=v[n]
      cov=(lo<=w0 && hi>=w1)?"full":"CLIPPED"
      printf "%.2f %.4f %.1f %d %d %s", cpus_full, cpus_full*1000/ints, 100*cpus_full/(w1-w0), med, mx, cov
    }' "$D/sample.tsv")"

  printf '%s\t%.1f\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.2f\t%s\t%s\t%s\n' \
    "$r" "$IPS" "$P50" "$P95" "$P99" "$CPUS" "$CPUMS" "$PCT" "$RSSI" "$RMED" "$RMAX" \
    "$INT" "$ER" "$PR" "$VD" "$GC" "$GS" "$ELS" "$COVER"
done
