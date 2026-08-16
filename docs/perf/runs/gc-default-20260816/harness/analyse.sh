#!/usr/bin/env bash
# analyse.sh — turn the per-arm acct.txt files into results.tsv, and assert the
# validity conditions the harness deliberately did NOT enforce inline.
#
# `-max-error-rate 1.0` follows the corpus convention so a transient blip does
# not discard an otherwise good arm; the error rate is asserted HERE instead. An
# arm that established fewer sessions than it requested measured a different
# workload and is marked invalid rather than quietly averaged in.
set -u
MB=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gcdef
OUT="$MB/results.tsv"

printf 'tag\tsessions\tgogc\tgomemlimit\twin_peak_rss_kb\tpg_peak_kb\ttput\testab\terr\tpatch\tload1\tgc_banner\tvalid\tverdict\n' >| "$OUT"

for d in "$MB"/runs/*/; do
  a="$d/acct.txt"; [ -f "$a" ] || continue
  get() { awk -v k="$1" '$1==k{$1=""; sub(/^ /,""); print; exit}' "$a"; }
  tag=$(get tag); n=$(get sessions)
  gogc=$(get env_gogc); gml=$(get env_gomemlimit)
  peak=$(get window_peak_rss_kb); pg=$(get pg_peak_rss_kb)
  tput=$(get tput); estab=$(get established); err=$(get error_rate); patch=$(get patch_rate)
  load1=$(get load1); aborted=$(get aborted); rc=$(get generator_rc)
  # The banner is read from the app's own log, not from acct.txt: the harness's
  # own grep anchored `^\[sky.gc\]` and the line carries a log timestamp, so it
  # recorded 'none' for every arm. The line itself was always there.
  banner=$(grep -m1 "sky.gc" "$d/app.log" 2>/dev/null | sed 's/.*\[sky\.gc\] //')

  valid=true; verdict=OK
  [ "$aborted" = "yes" ] && { valid=false; verdict="ABORTED_ON_RSS"; }
  [ "${rc:-1}" != "0" ]  && { valid=false; verdict="GENERATOR_RC_$rc"; }
  awk -v e="${err:-1}" 'BEGIN{exit !(e>0)}' && { valid=false; verdict="ERROR_RATE_$err"; }
  awk -v p="${patch:-0}" 'BEGIN{exit !(p<0.99)}' && { valid=false; verdict="PATCH_RATE_$patch"; }
  [ "${estab:-0}" != "$n" ] && { valid=false; verdict="ESTABLISHED_${estab}_OF_$n"; }
  [ -z "$banner" ] && { valid=false; verdict="NO_GC_BANNER"; }

  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$tag" "$n" "$gogc" "$gml" "$peak" "$pg" "$tput" "$estab" "$err" "$patch" \
    "$load1" "$banner" "$valid" "$verdict" >> "$OUT"
done

column -t -s$'\t' "$OUT"
echo
echo "invalid arms: $(awk -F'\t' 'NR>1 && $13=="false"' "$OUT" | wc -l | tr -d ' ')"

# The safety property, computed rather than asserted by hand.
echo
echo "== e2-small fit, from the measured arms =="
echo "   budget 1977 MiB; macOS/arm64 RSS ÷ 1.17 for a Linux estimate (borrowed"
echo "   from gcp-x86-capacity-20260816, NOT re-measured here); OS 256 MiB."
awk -F'\t' 'NR>1 && $1 ~ /^e2small/ && $13=="true" {
  app=$5/1024/1.17; pg=$6/1024; total=app+pg+256;
  printf "   %-14s app %7.0f MB  + pg %5.0f MB + OS 256 MB = %7.0f MB of 1977 MB  (%.0f%%)\n", $1, app, pg, total, total*100/1977
}' "$OUT"
