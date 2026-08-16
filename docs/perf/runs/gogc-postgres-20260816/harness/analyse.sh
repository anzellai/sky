#!/usr/bin/env bash
# analyse.sh — reduce every run dir to one row.
#
# Validity is asserted HERE, not at run time. The generator ran with
# -max-error-rate 1.0 (the corpus convention) so a transient blip does not
# discard an otherwise good arm; the price is that the error rate must be
# checked at analysis instead. An arm with error_rate > 0.01, patch_rate < 0.9,
# valid=false, fewer sessions established than requested, or an RSS abort is
# marked BAD in the last column rather than silently averaged in.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc
OUT="${1:-$BASE/results.tsv}"

printf 'tag\tsessions\tgogc\tgoml\ttput\twin_peak_rss_kb\twin_mean_rss_kb\tidle_rss_kb\tpg_peak_kb\tapp_cpu_s\testab\terr\tpatch\tvalid\tverdict\n' >| "$OUT"

for d in "$BASE"/runs/*/; do
  tag=$(basename "$d")
  J="$d/load.json"; A="$d/acct.txt"
  [ -f "$J" ] && [ -f "$A" ] || continue

  jnum() { tr ',' '\n' < "$J" | grep -o "\"$1\": *[0-9.eE+-]*" | head -1 | sed 's/.*: *//'; }
  jbool() { tr ',' '\n' < "$J" | grep -o "\"$1\": *[a-z]*" | head -1 | sed 's/.*: *//'; }
  ag() { awk -v k="$1" '$1==k{print $2}' "$A"; }

  req=$(jnum sessions_requested); est=$(jnum sessions_established)
  err=$(jnum error_rate); pat=$(jnum patch_rate); val=$(jbool valid); ab=$(ag aborted)

  verdict=OK
  [ "$val" != "true" ] && verdict=BAD:invalid
  awk -v e="${err:-1}"  'BEGIN{exit !(e>0.01)}' && verdict=BAD:errors
  awk -v p="${pat:-0}"  'BEGIN{exit !(p<0.9)}'  && verdict=BAD:patchrate
  [ "${est:-0}" != "${req:-1}" ] && verdict="BAD:estab-$est-of-$req"
  [ "$ab" = "yes" ] && verdict=BAD:rss-abort

  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$tag" "$(ag sessions)" "$(ag gogc)" "$(ag gomemlimit)" "$(jnum interactions_per_sec)" \
    "$(ag window_peak_rss_kb)" "$(ag window_mean_rss_kb)" "$(ag idle_rss_kb)" \
    "$(ag pg_peak_rss_kb)" "$(ag app_cpu_delta_s)" "$est" "$err" "$pat" "$val" "$verdict" >> "$OUT"
done

column -t -s$'\t' "$OUT"
