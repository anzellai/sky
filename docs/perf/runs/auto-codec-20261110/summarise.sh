#!/usr/bin/env bash
# Median ms per op and allocs per op, per target, op, shape and size, from
# run.sh's results. Usage: summarise.sh <results-dir>
set -euo pipefail
DIR="${1:?results dir}"
printf '| target | op | shape | MB | reference ms | generated ms | speed-up | reference allocs | generated allocs |\n|---|---|---|---|---|---|---|---|---|\n'
for target in native wasm; do
  for op in encode decode; do
    for shape in list dict blob; do
      for mb in 1 3; do
        row=()
        for mode in reference generated; do
          f="$DIR/$target-$mode-$shape-$mb.txt"
          stats=$(grep "^BenchmarkAutoCodec/$op/" "$f" | awk '{for(i=1;i<=NF;i++){if($(i+1)=="ns/op")ns=$i; if($(i+1)=="allocs/op")al=$i}; print ns, al}' | sort -n | awk '{a[NR]=$1; b[NR]=$2} END {m=int((NR+1)/2); printf "%.2f %d", a[m]/1e6, b[m]}')
          row+=($stats)
        done
        speed=$(awk -v r="${row[0]}" -v g="${row[2]}" 'BEGIN {printf "%.1fx", r/g}')
        printf '| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n' "$target" "$op" "$shape" "$mb" "${row[0]}" "${row[2]}" "$speed" "${row[1]}" "${row[3]}"
      done
    done
  done
done
