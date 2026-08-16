#!/usr/bin/env bash
# memsum.sh — one row per memory run. RSS UNDER LOAD, which is the number a
# capacity table needs, alongside the post-GC and post-settle figures so the
# reader can see how much of it is headroom rather than retention.
#
# The archived 336 kB/session is `(P1 - P0).heap_alloc / N` with the sessions
# IDLE after two forced GCs. That is a correct retention measurement and the
# wrong sizing input: the same runs show heap_alloc at 169-237 MB under load
# against 39.9 MB idle, and RSS settling at 358-380 MB and never returning.
set -euo pipefail

jnum() {
  tr ',' '\n' < "$1" | awk -v k="\"$2\"" '
    { line = $0; gsub(/^[ \t{]+/, "", line)
      if (index(line, k ":") == 1) { v = substr(line, length(k)+2); gsub(/[ \t}]/, "", v); print v; exit } }'
}
kv() { awk -v k="$2" '$1==k {print $2; exit}' "$1"; }

printf "run\tstore\tN\testab\ttput\tpatch_rate\trss_load_MB\trss_gc_MB\trss_settle_MB\theap_load_MB\theap_settle_MB\tkB_per_sess_load\tgoroutines\n"
for d in "$@"; do
  [ -d "$d" ] || continue
  if [ -f "$d/REJECTED" ]; then echo "$(basename "$d")	REJECTED: $(cat "$d/REJECTED")" >&2; continue; fi
  [ -f "$d/snapshot.txt" ] || { echo "$(basename "$d")	no snapshot" >&2; continue; }

  n=$(kv "$d/env.txt" sessions)
  # The store the app ACTUALLY used, from its own banner -- not from what the
  # run asked for. A postgres DSN the runtime cannot parse falls back to
  # memory and then serves normally, so a run can be entirely valid and
  # entirely mislabelled.
  store=$(awk -F'session store: ' '/session store: /{split($2,a," "); print a[1]; exit}' "$d/app.log")
  estab=$(kv "$d/snapshot.txt" established_conns_incl_header)
  rssl=$(kv "$d/snapshot.txt" rss_kb_under_load_nogc)
  rssg=$(kv "$d/snapshot.txt" rss_kb_under_load_after_gc)
  rsss=$(kv "$d/snapshot.txt" rss_kb_after_settle)
  hl=$(jnum "$d/memstats-load-nogc.json" heap_alloc)
  hs=$(jnum "$d/memstats-after.json" heap_alloc 2>/dev/null || echo 0)
  gor=$(jnum "$d/memstats-load-nogc.json" num_goroutine)
  tput=$(jnum "$d/load.json" interactions_per_sec 2>/dev/null || echo 0)
  pr=$(jnum "$d/load.json" patch_rate 2>/dev/null || echo 0)

  awk -v r="$(basename "$(dirname "$d")")/$(basename "$d")" -v st="${store:-UNKNOWN}" -v n="$n" \
      -v e="$estab" -v t="$tput" -v pr="$pr" -v rl="$rssl" -v rg="$rssg" -v rs="$rsss" \
      -v hl="$hl" -v hs="$hs" -v g="$gor" 'BEGIN{
    printf "%s\t%s\t%s\t%s\t%.1f\t%.3f\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%.0f\t%s\n",
      r, st, n, e, t, pr, rl/1024, rg/1024, rs/1024, hl/1048576, hs/1048576,
      (n>0 ? rl/n : 0), g
  }'
done
